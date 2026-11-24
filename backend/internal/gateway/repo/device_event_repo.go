package repo

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// 读取 gateway 持久设备事件并更新处理标记；原始载荷由调用方解释协议含义。
//
// 处理标记保留 processed_at IS NULL 条件，重放不重复更新已处理记录。

// DeviceFaultEvent 是一条未处理的设备故障事件。
type DeviceFaultEvent struct {
	ID         uint64    `json:"id"`          // 事件自增主键
	EventKey   string    `json:"event_key"`   // 幂等事件键
	DeviceID   string    `json:"device_id"`   // 持久设备编号，用于核对协议载荷
	ReceivedAt time.Time `json:"received_at"` // 持久接收时刻，毫秒精度
	Payload    string    `json:"payload"`     // 协议事件 JSON
}

// ListUnprocessedFaults 按 ID 升序读取尚未处理的 DC589 故障事件。
// SQL 内 event_type = 'fault' 对应 protocol.Fault、protocol_name = 'dc589' 对应
// protocol.AdapterIDDC589；这两个列是 VARCHAR 开放集合，
// 此处只按已知取值筛选，不拒绝其他值。
func ListUnprocessedFaults(ctx context.Context, db *gorm.DB, limit int) ([]DeviceFaultEvent, error) {
	rows := []DeviceFaultEvent{}
	err := db.WithContext(ctx).Table("device_event").
		Select("id, event_key, device_id, received_at, event_json AS payload").
		Where("event_type = 'fault' AND protocol_name = 'dc589' AND processed_at IS NULL").
		Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

// MarkEventsProcessed 在同一事务中标记尚未处理的事件，重复请求不重复计数。
// 写入或提交失败时整批回滚；返回计数只有在 err=nil 时表示已提交。
func MarkEventsProcessed(ctx context.Context, db *gorm.DB, ids []uint64) (int64, error) {
	marked := int64(0)
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		count, err := markEventsProcessed(tx, ids)
		marked = count
		return err
	})

	return marked, err
}

// ErrHeartbeatWatermarkUnreadable 表示键或显式时间给出的查询起点为零，不能继续查询后续心跳。
var ErrHeartbeatWatermarkUnreadable = errors.New("heartbeat watermark unreadable")

// HeartbeatEvidence 返回查询起点和该时刻之后最新的心跳。
type HeartbeatEvidence struct {
	// Watermark 是指定事件的接收时间或传入时间；事件不存在时为 nil。
	Watermark *time.Time
	// Heartbeat 是查询起点之后最新的 DC589 心跳；没有记录时为 nil。
	Heartbeat *HeartbeatAfter
}

// ReadLatestHeartbeat 优先使用 afterKey 对应事件的接收时间，否则使用 after。
// 指定事件不存在时返回空结果；起点为零时报错，后续无心跳时仍返回起点。
func ReadLatestHeartbeat(
	ctx context.Context,
	db *gorm.DB,
	deviceID string,
	afterKey string,
	after time.Time,
) (HeartbeatEvidence, error) {
	var watermark time.Time
	if afterKey != "" {
		receivedAt, err := findEventReceivedAt(ctx, db, deviceID, afterKey)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return HeartbeatEvidence{}, nil
			}
			return HeartbeatEvidence{}, err
		}
		watermark = receivedAt
	} else {
		watermark = after
	}
	if watermark.IsZero() {
		return HeartbeatEvidence{}, ErrHeartbeatWatermarkUnreadable
	}
	row, err := latestHeartbeatAfter(ctx, db, deviceID, watermark.UTC())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return HeartbeatEvidence{Watermark: &watermark}, nil
		}
		return HeartbeatEvidence{}, err
	}
	return HeartbeatEvidence{Watermark: &watermark, Heartbeat: &row}, nil
}

// markEventsProcessed 在调用方事务内标记仍未处理的事件，返回实际影响行数；重复请求不重复计数。
func markEventsProcessed(tx *gorm.DB, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var pending []struct {
		// ID device_event 表的自增主键。
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

// findEventReceivedAt 查询指定设备事件的接收时间。
func findEventReceivedAt(ctx context.Context, db *gorm.DB, deviceID, eventKey string) (time.Time, error) {
	var row struct {
		// ReceivedAt 事件接收时刻。
		ReceivedAt time.Time `gorm:"column:received_at"`
	}
	err := db.WithContext(ctx).Table("device_event").Select("received_at").
		Where("device_id = ? AND event_key = ?", deviceID, eventKey).Take(&row).Error
	return row.ReceivedAt, err
}

// HeartbeatAfter 是某设备在某个时刻之后最新的一次心跳。
type HeartbeatAfter struct {
	ReceivedAt time.Time `gorm:"column:received_at"` // 心跳持久时刻
	Payload    []byte    `gorm:"column:event_json"`  // 协议载荷字节
}

// latestHeartbeatAfter 按接收时间、ID 倒序读取起点之后的最新 DC589 心跳。
// SQL 内 event_type = 'heartbeat' 对应 protocol.Heartbeat、protocol_name = 'dc589'
// 对应 protocol.AdapterIDDC589；字面量写在 SQL 语法内，不绑定参数。
// 该列是 VARCHAR 开放集合，此处只按已知取值筛选，不拒绝其他值。
func latestHeartbeatAfter(ctx context.Context, db *gorm.DB, deviceID string, after time.Time) (HeartbeatAfter, error) {
	var row HeartbeatAfter
	err := db.WithContext(ctx).Table("device_event").
		Select("received_at, event_json").
		Where("device_id = ? AND protocol_name = 'dc589' AND event_type = 'heartbeat' AND received_at > ?", deviceID, after).
		Order("received_at DESC, id DESC").Take(&row).Error
	return row, err
}

// HeartbeatSample 是一条心跳事件的原始载荷。
type HeartbeatSample struct {
	// ID device_event 表的自增主键，用作心跳分页游标。
	ID uint64 `gorm:"column:id"`
	// EventJSON 持久协议事件 JSON 字节。
	EventJSON []byte `gorm:"column:event_json"`
}

// ListHeartbeatsInWindow 按 ID 升序查询时间闭区间内的心跳，并多取一条判断是否还有下一页。
// SQL 内 event_type='heartbeat' 对应 protocol.Heartbeat；该列是 VARCHAR 开放集合，
// 此处只按已知取值筛选，不据此拒绝其他值。
func ListHeartbeatsInWindow(ctx context.Context, db *gorm.DB, device string, from, to time.Time, afterID uint64, limit int) ([]HeartbeatSample, error) {
	query := db.WithContext(ctx).Table("device_event").Select("id, event_json").
		Where("device_id=? AND event_type='heartbeat' AND received_at>=? AND received_at<=?", device, from.UTC(), to.UTC())
	if afterID > 0 {
		query = query.Where("id > ?", afterID)
	}
	rows := []HeartbeatSample{}
	err := query.Order("id ASC").Limit(limit + 1).Find(&rows).Error
	return rows, err
}
