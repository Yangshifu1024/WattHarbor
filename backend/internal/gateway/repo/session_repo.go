package repo

import (
	"context"
	"database/sql"
	"time"

	"wattharbor/internal/domain/money"
	"wattharbor/internal/protocol"

	"gorm.io/gorm"
)

// 设备连接会话先读取候选，再按 id 与 created_month 完整复合主键关闭。
// created_month 同时用于分区定位，不能只按 id 更新；本操作不处理充电命令会话。

// IdleSessionRow 返回超过静默阈值且尚未关闭的数据库连接记录。
type IdleSessionRow struct {
	// SessionID 设备连接会话标识，不是 START 或 STOP 命令会话。
	SessionID string `gorm:"column:session_id"`
	// DeviceID 设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// Protocol 连接传输方式，对应 device_session.protocol（tcp/mqtt）。
	Protocol protocol.Transport `gorm:"column:protocol"`
	// RemoteAddr 连接远端地址。
	RemoteAddr string `gorm:"column:remote_addr"`
	// StartedAt 设备连接开始时刻，保留数据库映射，不转换时区。
	StartedAt time.Time `gorm:"column:started_at"`
	// LastActiveAt 设备连接最后活动时刻，作为静默阈值与列表排序依据。
	LastActiveAt time.Time `gorm:"column:last_active_at"`
}

// ListIdleSessions 查询超过静默阈值的未关闭记录，按最后活动时间升序返回。
func ListIdleSessions(
	ctx context.Context,
	db *gorm.DB,
	idleThreshold time.Duration,
	limit int,
) ([]IdleSessionRow, error) {
	var rows []IdleSessionRow
	err := idleSessionsQuery(ctx, db, idleThreshold).
		Select(
			"session_id",
			"device_id",
			"protocol",
			"remote_addr",
			"started_at",
			"last_active_at",
		).
		Order("last_active_at ASC").Limit(limit).Scan(&rows).Error
	return rows, err
}

// CloseIdleSessions 在同一事务内按 ID 和月份关闭静默记录；at 是记录的关闭时间。
// 任一更新失败整批回滚，计数仅在 err=nil 时表示已提交；不会断开 TCP 连接。
func CloseIdleSessions(
	ctx context.Context,
	db *gorm.DB,
	idleThreshold time.Duration,
	limit int,
	at time.Time,
) (int64, error) {
	// 在同一事务中读取并关闭候选会话；更新再次检查 ended_at IS NULL，避免覆盖并发正常关闭的记录。
	var closed int64
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []sessionKey
		if err := idleSessionsQuery(ctx, tx, idleThreshold).
			Select("id", "created_month").Limit(limit).Scan(&candidates).Error; err != nil {
			return err
		}
		for _, key := range candidates {
			affected, err := closeAbandonedSession(tx, key, at)
			if err != nil {
				return err
			}
			closed += affected
		}
		return nil
	})

	return closed, err
}

// sessionKey 是设备连接会话的完整复合主键；CreatedMonth 同时定位分区。
type sessionKey struct {
	// ID device_session 表的自增 id，与 CreatedMonth 组成完整复合主键。
	ID uint64 `gorm:"column:id"`
	// CreatedMonth 分区日期，与记录标识共同定位分区。
	CreatedMonth time.Time `gorm:"column:created_month"`
}

// idleSessionsQuery 筛选未关闭且最后活动时间早于当前时间减静默阈值的记录。
func idleSessionsQuery(ctx context.Context, db *gorm.DB, idleThreshold time.Duration) *gorm.DB {
	return db.WithContext(ctx).Table("device_session").
		Where("ended_at IS NULL AND last_active_at < ?", time.Now().UTC().Add(-idleThreshold))
}

// closeAbandonedSession 按 id/created_month 关闭仍开放的数据库会话记录，不提交事务或关闭 TCP 连接。
func closeAbandonedSession(tx *gorm.DB, key sessionKey, at time.Time) (int64, error) {
	month := time.Date(key.CreatedMonth.UTC().Year(), key.CreatedMonth.UTC().Month(), key.CreatedMonth.UTC().Day(), 0, 0, 0, 0, time.UTC)
	// close_reason 是 VARCHAR 开放集合；网关侧补记的断开原因使用 protocol.CloseAbandoned。
	res := tx.Table("device_session").
		Where("id = ? AND created_month = ? AND ended_at IS NULL", key.ID, month).
		Updates(map[string]any{"ended_at": at, "close_reason": protocol.CloseAbandoned})
	return res.RowsAffected, res.Error
}

// DeviceSummaryRow 是设备摘要的一行，联表带出厂商名。
type DeviceSummaryRow struct {
	// DeviceID 设备业务编号。
	DeviceID string `json:"device_id" gorm:"column:device_id"`
	// VendorName 厂商名称，读取时保留可空映射。
	VendorName *string `json:"vendor_name" gorm:"column:vendor_name"`
	// LastHeartbeatAt 最后心跳时刻，可空。
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at" gorm:"column:last_heartbeat_at"`
	// SignalStrength 设备信号强度，保留未知可空映射。
	SignalStrength *uint8 `json:"signal_strength" gorm:"column:signal_strength"`
	// SignalAt 信号上报时刻，可空。
	SignalAt *time.Time `json:"signal_at" gorm:"column:signal_at"`
	// Ports 是该设备下各端口的上报状态，由 ListDevicePortStatuses 取回后回填。
	Ports []DevicePortStatusRow `json:"ports" gorm:"-"`
}

// ListDeviceSummaries 批量查询未删除设备的厂商、心跳和信号；缺少数据保持 NULL。
func ListDeviceSummaries(ctx context.Context, db *gorm.DB, deviceIDs []string) ([]DeviceSummaryRow, error) {
	rows := []DeviceSummaryRow{}
	err := db.WithContext(ctx).Table("device d").
		Select("d.device_id,v.vendor_name,d.last_heartbeat_at,d.signal_strength,d.signal_at").
		Joins("LEFT JOIN vendor v ON v.id=d.vendor_id AND v.deleted_at IS NULL").
		Where("d.device_id IN ? AND d.deleted_at IS NULL", deviceIDs).Scan(&rows).Error
	return rows, err
}

// DevicePortStatusRow 是设备一个端口的上报状态。
type DevicePortStatusRow struct {
	// DeviceID 设备业务编号。
	DeviceID string `json:"-" gorm:"column:device_id"`
	// PortNo 协议端口序号。
	PortNo uint8 `json:"port_no" gorm:"column:port_no"`
	// StatusCode 是设备上报的端口状态；nil 表示未知。
	StatusCode *uint8 `json:"status_code" gorm:"column:reported_status"`
	// StatusAt 端口状态上报时刻，可空。
	StatusAt *time.Time `json:"status_at" gorm:"column:reported_status_at"`
	// Status 为端口业务状态，区别于协议原始读数。
	Status money.PortStatus `json:"-" gorm:"column:status"`
	// CurrentOrderID 为占用端口的订单号，NULL 表示无占用。
	CurrentOrderID sql.NullString `json:"-" gorm:"column:current_order_id"`
	// LastSeenAt 为网关最近可见时间，NULL 表示从未通信。
	LastSeenAt sql.NullTime `json:"-" gorm:"column:last_seen_at"`
	// Enabled 表示设备和所属厂商同时启用。
	Enabled bool `json:"-" gorm:"column:enabled"`
}

// Available 根据本批摘要的查询时刻，按扫码准入规则判断可用性。
func (r DevicePortStatusRow) Available(now time.Time) bool {
	return r.Enabled && portAvailable(r.Status, r.CurrentOrderID, portDeviceOnline(r.LastSeenAt, now))
}

// ListDevicePortStatuses 查询设备未删除端口的上报状态；未上报保持 NULL。
func ListDevicePortStatuses(ctx context.Context, db *gorm.DB, deviceIDs []string) ([]DevicePortStatusRow, error) {
	rows := []DevicePortStatusRow{}
	err := db.WithContext(ctx).Table("device_port p").
		Select("p.device_id,p.port_no,p.reported_status,p.reported_status_at,p.status,p.current_order_id,d.last_seen_at,IF(d.status='enabled' AND v.status='enabled',1,0) AS enabled").
		Joins("JOIN device d ON d.device_id=p.device_id AND d.deleted_at IS NULL").
		Joins("LEFT JOIN vendor v ON v.id=d.vendor_id AND v.deleted_at IS NULL").
		Where("p.device_id IN ? AND p.deleted_at IS NULL", deviceIDs).
		Order("p.device_id,p.port_no").Scan(&rows).Error
	return rows, err
}
