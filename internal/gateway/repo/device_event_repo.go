package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 设备事件的读取与处理标记。这些事件由 central 侧的告警同步与停机判定消费，
// 事件本身仍由 gateway 落库——设备接入的事实只有它有。
//
// 处理标记保留 processed_at IS NULL 条件，重放不重复推进已处理记录。

// DeviceFaultEvent 是一条未处理的设备故障事件。
type DeviceFaultEvent struct {
	ID         uint64    `json:"id"`          // 原事件自增主键
	EventKey   string    `json:"event_key"`   // 原幂等事件键
	DeviceID   string    `json:"device_id"`   // 原持久设备身份，用于核对协议载荷
	ReceivedAt time.Time `json:"received_at"` // 原持久接收时刻，毫秒精度
	Payload    string    `json:"payload"`     // 原协议事件JSON
}

// ListUnprocessedFaults 接收 ctx、db 与 limit，按 id 升序读取未处理 dc589 故障原证据；返回列表与 SQL 错误，无标记写入。
func ListUnprocessedFaults(ctx context.Context, db *gorm.DB, limit int) ([]DeviceFaultEvent, error) {
	rows := []DeviceFaultEvent{}
	err := db.WithContext(ctx).Table("device_event").
		Select("id, event_key, device_id, received_at, event_json AS payload").
		Where("event_type = 'fault' AND protocol_name = 'dc589' AND processed_at IS NULL").
		Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

// MarkEventsProcessed 接收所属 tx 与 ids，读取仍未处理候选并按原 IS NULL 条件更新处理时刻；返回首次推进数量或 SQL 错误，空输入及重放零行，不自行提交。
func MarkEventsProcessed(tx *gorm.DB, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var pending []struct {
		// ID device_event 表的原自增主键。
		ID uint64
	}
	if err := tx.Table("device_event").Select("id").
		Where("id IN ? AND processed_at IS NULL", ids).Find(&pending).Error; err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		return 0, nil
	}
	res := tx.Table("device_event").Where("id IN ? AND processed_at IS NULL", ids).
		Update("processed_at", gorm.Expr("UTC_TIMESTAMP(3)"))
	return res.RowsAffected, res.Error
}

// FindEventReceivedAt 接收 ctx、db、deviceID 与 eventKey，读取原持久接收时刻；返回时刻或缺失、SQL 错误，无写入。
func FindEventReceivedAt(ctx context.Context, db *gorm.DB, deviceID, eventKey string) (time.Time, error) {
	var row struct {
		// ReceivedAt 原事件接收时刻。
		ReceivedAt time.Time `gorm:"column:received_at"`
	}
	err := db.WithContext(ctx).Table("device_event").Select("received_at").
		Where("device_id = ? AND event_key = ?", deviceID, eventKey).Take(&row).Error
	return row.ReceivedAt, err
}

// HeartbeatAfter 是某设备在某个时刻之后最新的一次心跳。
type HeartbeatAfter struct {
	ReceivedAt time.Time `gorm:"column:received_at"` // 原心跳持久时刻
	Payload    []byte    `gorm:"column:event_json"`  // 原协议载荷字节
}

// LatestHeartbeatAfter 接收 ctx、db、deviceID 与 after，读取严格晚于该时刻的最新 dc589 心跳，按时刻及 id 倒序；返回原证据或缺失、SQL 错误，无写入。
func LatestHeartbeatAfter(ctx context.Context, db *gorm.DB, deviceID string, after time.Time) (HeartbeatAfter, error) {
	var row HeartbeatAfter
	err := db.WithContext(ctx).Table("device_event").
		Select("received_at, event_json").
		Where("device_id = ? AND protocol_name = 'dc589' AND event_type = 'heartbeat' AND received_at > ?", deviceID, after).
		Order("received_at DESC, id DESC").Take(&row).Error
	return row, err
}
