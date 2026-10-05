package repo

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// 读取 gateway 持久设备事件并推进处理标记；原始载荷由消费方解释协议含义。
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

// MarkEventsProcessed 接收 ctx、db 与原 ids，将候选读取和原批量条件更新放在同一请求事务。
// 返回实际影响行数及 SQL/提交错误，空输入和重放可为零；计数仅在 err=nil 时代表提交结果，失败回滚。
// 保留普通候选读取及原 ids 更新，不新增锁读、排序、协议筛选或重试。
func MarkEventsProcessed(ctx context.Context, db *gorm.DB, ids []uint64) (int64, error) {
	marked := int64(0)
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		count, err := markEventsProcessed(tx, ids)
		marked = count
		return err
	})

	return marked, err
}

// ErrHeartbeatWatermarkUnreadable 表示原键或显式时间给出的水印为零，不能继续查询后续心跳。
var ErrHeartbeatWatermarkUnreadable = errors.New("heartbeat watermark unreadable")

// HeartbeatEvidence 是原水印与后续最新心跳的持久读取投影，不解释协议恢复状态。
type HeartbeatEvidence struct {
	// Watermark 原键接收时刻或显式时间，nil 表示键不存在；保留输入时区用于原响应映射。
	Watermark *time.Time
	// Heartbeat 严格晚于水印的最新 dc589 心跳原证据，nil 表示没有后续心跳。
	Heartbeat *HeartbeatAfter
}

// ReadLatestHeartbeat 接收 ctx、db、deviceID、afterKey 与 after，非空键优先定位水印，缺键即返回空投影。
// 零水印返回 ErrHeartbeatWatermarkUnreadable；已有水印时用 UTC 查询原后续心跳，缺心跳保留水印。
// 返回原持久载荷和时刻或 SQL 错误，不写入、加锁、重试或解释协议恢复状态。
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

// markEventsProcessed 接收所属 tx 与 ids，读取仍未处理候选并按原 ids 和 IS NULL 条件批量更新处理时刻；返回实际影响行数或 SQL 错误，空输入及重放零行，不自行提交。
func markEventsProcessed(tx *gorm.DB, ids []uint64) (int64, error) {
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

// findEventReceivedAt 接收 ctx、db、deviceID 与 eventKey，读取原持久接收时刻；返回时刻或缺失、SQL 错误，无写入。
func findEventReceivedAt(ctx context.Context, db *gorm.DB, deviceID, eventKey string) (time.Time, error) {
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

// latestHeartbeatAfter 接收 ctx、db、deviceID 与 after，读取严格晚于该时刻的最新 dc589 心跳，按时刻及 id 倒序；返回原证据或缺失、SQL 错误，无写入。
func latestHeartbeatAfter(ctx context.Context, db *gorm.DB, deviceID string, after time.Time) (HeartbeatAfter, error) {
	var row HeartbeatAfter
	err := db.WithContext(ctx).Table("device_event").
		Select("received_at, event_json").
		Where("device_id = ? AND protocol_name = 'dc589' AND event_type = 'heartbeat' AND received_at > ?", deviceID, after).
		Order("received_at DESC, id DESC").Take(&row).Error
	return row, err
}
