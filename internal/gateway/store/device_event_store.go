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
	ID         uint64    `json:"id"`
	EventKey   string    `json:"event_key"`
	ReceivedAt time.Time `json:"received_at"`
	Payload    string    `json:"payload"`
}

// ListUnprocessedFaults 返回未处理的 dc589 故障事件，按 id 升序。
func ListUnprocessedFaults(ctx context.Context, db *gorm.DB, limit int) ([]DeviceFaultEvent, error) {
	rows := []DeviceFaultEvent{}
	err := db.WithContext(ctx).Table("device_event").
		Select("id, event_key, received_at, event_json AS payload").
		Where("event_type = 'fault' AND protocol_name = 'dc589' AND processed_at IS NULL").
		Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

// MarkEventsProcessed 标记一批事件已处理，返回真正被标记的条数。
// 调用方应先确认这批事件确实存在——全部已被别人标过时返回 0 是正常结果。
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

// FindEventReceivedAt 取一条事件的上报时刻。取不到返回 gorm.ErrRecordNotFound。
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
	ReceivedAt time.Time `gorm:"column:received_at"`
	Payload    []byte    `gorm:"column:event_json"`
}

// LatestHeartbeatAfter 返回设备在 after 之后的最新一次 dc589 心跳。
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
