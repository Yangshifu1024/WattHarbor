package store

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 设备事件的读取与处理标记。这些事件由 central 侧的告警同步与停机判定消费，
// 事件本身仍由 gateway 落库——设备接入的事实只有它有。
//
// 处理标记是幂等的：条件带 processed_at IS NULL，重复标记同一批事件只影响
// 一次。否则两个消费者各标一次，attempts 之类的计数会被重复累加。

// DeviceFaultEvent 是一条未处理的设备故障事件。
type DeviceFaultEvent struct {
	ID         uint64    `json:"id"`          // 原事件自增主键
	EventKey   string    `json:"event_key"`   // 原幂等事件键
	DeviceID   string    `json:"device_id"`   // 原持久设备身份，用于核对协议载荷
	ReceivedAt time.Time `json:"received_at"` // 原持久接收时刻，毫秒精度
	Payload    string    `json:"payload"`     // 原协议事件JSON
}

// ListUnprocessedFaults 在ctx下使用db读取limit原未处理dc589故障，返回升序证据或SQL错误，无标记写入。
func ListUnprocessedFaults(ctx context.Context, db *gorm.DB, limit int) ([]DeviceFaultEvent, error) {
	rows := []DeviceFaultEvent{}
	err := db.WithContext(ctx).Table("device_event").
		Select("id, event_key, device_id, received_at, event_json AS payload").
		Where("event_type = 'fault' AND protocol_name = 'dc589' AND processed_at IS NULL").
		Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

// MarkEventsProcessed 在tx下标记ids原未处理主键，返回实际首次推进数或SQL错误，不自行提交。
// UPDATE使用当前条件；并发已标记或回复丢失重放返回0，不把原证据替换为新身份。
func MarkEventsProcessed(tx *gorm.DB, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var pending []struct{ ID uint64 }
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

// FindEventReceivedAt 在ctx下使用db按deviceID／eventKey读原接收时刻，返回事实、缺失或SQL错误，无写入。
func FindEventReceivedAt(ctx context.Context, db *gorm.DB, deviceID, eventKey string) (time.Time, error) {
	var row struct {
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

// LatestHeartbeatAfter 在ctx下用db读取deviceID严格晚于after的最新dc589心跳，返回原时刻／载荷、缺失或SQL错误，无写入。
//
// 这条查询是恢复判定的证据来源：告警自动核销要拿它和故障时刻比对，早于故障
// 的心跳不构成"设备已恢复"的证据。
func LatestHeartbeatAfter(ctx context.Context, db *gorm.DB, deviceID string, after time.Time) (HeartbeatAfter, error) {
	var row HeartbeatAfter
	err := db.WithContext(ctx).Table("device_event").
		Select("received_at, event_json").
		Where("device_id = ? AND protocol_name = 'dc589' AND event_type = 'heartbeat' AND received_at > ?", deviceID, after).
		Order("received_at DESC, id DESC").Take(&row).Error
	return row, err
}
