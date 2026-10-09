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

// ListUnprocessedFaults 按 id 升序读取未处理 dc589 故障证据；返回列表与 SQL 错误，不更新处理标记。
func ListUnprocessedFaults(ctx context.Context, db *gorm.DB, limit int) ([]DeviceFaultEvent, error) {
	rows := []DeviceFaultEvent{}
	err := db.WithContext(ctx).Table("device_event").
		Select("id, event_key, device_id, received_at, event_json AS payload").
		Where("event_type = 'fault' AND protocol_name = 'dc589' AND processed_at IS NULL").
		Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

// MarkEventsProcessed 将候选读取和批量条件更新放在同一请求事务。
// 返回实际影响行数及 SQL/提交错误，空输入和重放可为零；计数仅在 err=nil 时代表提交结果，失败回滚。
// 保留普通候选读取及 ids 更新，不新增锁读、排序、协议筛选或重试。
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

// HeartbeatEvidence 是查询起点与后续最新心跳的持久查询结果，不解释协议恢复状态。
type HeartbeatEvidence struct {
	// Watermark 键接收时刻或显式时间，nil 表示键不存在；保留输入时区用于响应映射。
	Watermark *time.Time
	// Heartbeat 严格晚于查询起点的最新 dc589 心跳证据，nil 表示没有后续心跳。
	Heartbeat *HeartbeatAfter
}

// ReadLatestHeartbeat 非空键优先定位查询起点，缺键即返回空结果。
// 零查询起点返回 ErrHeartbeatWatermarkUnreadable；已有查询起点时用 UTC 查询后续心跳，缺心跳保留查询起点。
// 返回持久载荷和时刻或 SQL 错误，不写入、加锁、重试或解释协议恢复状态。
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

// findEventReceivedAt 读取持久接收时刻；返回时刻或缺失、SQL 错误，不写库。
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

// latestHeartbeatAfter 读取严格晚于该时刻的最新 dc589 心跳，按时刻及 id 倒序；返回证据或缺失、SQL 错误，不写库。
func latestHeartbeatAfter(ctx context.Context, db *gorm.DB, deviceID string, after time.Time) (HeartbeatAfter, error) {
	var row HeartbeatAfter
	err := db.WithContext(ctx).Table("device_event").
		Select("received_at, event_json").
		Where("device_id = ? AND protocol_name = 'dc589' AND event_type = 'heartbeat' AND received_at > ?", deviceID, after).
		Order("received_at DESC, id DESC").Take(&row).Error
	return row, err
}
