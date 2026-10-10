package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"chargepilot/internal/platform/dbutil"
	"chargepilot/internal/protocol"
)

// deviceSessionRow 映射 device_session；created_month 为分区键和联合主键的一部分，读写必须保留。
type deviceSessionRow struct {
	// ID device_session 表的自增 id，与 CreatedMonth 组成库内完整复合主键。
	ID uint64 `gorm:"column:id;primaryKey"`
	// SessionID 设备连接会话标识，与开始日期对应的 CreatedMonth 定位连接审计记录。
	SessionID string `gorm:"column:session_id"`
	// DeviceID 设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// Protocol 是连接传输方式，对应 device_session.protocol（tcp/mqtt）。
	Protocol protocol.Transport `gorm:"column:protocol"`
	// RemoteAddr 设备连接远端地址。
	RemoteAddr string `gorm:"column:remote_addr"`
	// StartedAt 设备连接开始时刻。
	StartedAt time.Time `gorm:"column:started_at"`
	// LastActiveAt 设备连接最后活动时刻。
	LastActiveAt time.Time `gorm:"column:last_active_at"`
	// EndedAt 设备连接结束时刻。
	EndedAt time.Time `gorm:"column:ended_at"`
	// CloseReason 设备连接关闭原因，对应 device_session.close_reason。
	// 该列是 VARCHAR 开放集合，存量行可能有 protocol.CloseReason 常量以外的值，
	// 读取与写入均不做统一 Valid 拒绝。
	CloseReason protocol.CloseReason `gorm:"column:close_reason"`
	// BytesIn 连接累计接收字节数。
	BytesIn int64 `gorm:"column:bytes_in"`
	// BytesOut 连接累计发送字节数。
	BytesOut int64 `gorm:"column:bytes_out"`
	// FramesIn 连接累计接收帧数。
	FramesIn int64 `gorm:"column:frames_in"`
	// FramesOut 连接累计发送帧数。
	FramesOut int64 `gorm:"column:frames_out"`
	// created_month 为 DATE，查询得到时间值，写入时格式化为日期。
	CreatedMonth time.Time `gorm:"column:created_month"`
}

// TableName 返回固定表名 device_session，供 GORM 定位模型。
func (deviceSessionRow) TableName() string { return "device_session" }

// RecordSession 按会话号和开始月份更新或新增连接结束记录。
// 已有记录只更新结束和流量字段；月份始终取开始时间，跨月结束也使用同一分区。
func (s MySQLSink) RecordSession(ctx context.Context, record protocol.SessionRecord) error {
	if s.DB == nil {
		return errors.New("gateway database is unavailable")
	}
	if record.SessionID == "" || record.DeviceID == "" {
		return errors.New("session record requires session and device identity")
	}
	month := dbutil.MonthStartAt(record.StartedAt)
	row := deviceSessionRow{
		SessionID:    record.SessionID,
		DeviceID:     record.DeviceID,
		Protocol:     record.Transport,
		RemoteAddr:   record.RemoteAddr,
		StartedAt:    record.StartedAt,
		LastActiveAt: record.LastActive,
		EndedAt:      record.EndedAt,
		CloseReason:  record.CloseReason,
		BytesIn:      record.BytesIn,
		BytesOut:     record.BytesOut,
		FramesIn:     record.FramesIn,
		FramesOut:    record.FramesOut,
		CreatedMonth: month,
	}
	// 显式检查记录存在性并分别更新或插入，避免依赖分区表的 GORM 冲突目标推导。
	var existing int64
	if err := s.DB.WithContext(ctx).Model(&deviceSessionRow{}).
		Where("session_id = ? AND created_month = ?", row.SessionID, month).
		Count(&existing).Error; err != nil {
		return fmt.Errorf("look up device session: %w", err)
	}
	if existing > 0 {
		if err := s.DB.WithContext(ctx).Model(&deviceSessionRow{}).
			Where("session_id = ? AND created_month = ?", row.SessionID, month).
			Updates(map[string]any{
				"ended_at":       row.EndedAt,
				"last_active_at": row.LastActiveAt,
				"close_reason":   row.CloseReason,
				"bytes_in":       row.BytesIn,
				"bytes_out":      row.BytesOut,
				"frames_in":      row.FramesIn,
				"frames_out":     row.FramesOut,
			}).Error; err != nil {
			return fmt.Errorf("update device session: %w", err)
		}
		return nil
	}
	if err := s.DB.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("insert device session: %w", err)
	}
	return nil
}
