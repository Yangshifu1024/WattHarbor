package store

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 刷卡事件的投递与回复冻结。事件先落 device_event，刷卡侧的回复要求
// 在事件被消费前冻结下来——回复迟到时设备可能已经超时，重放没有意义。
//
// 冻结是首写生效：已有回复的事件不再被覆盖，接口据此回报 stored:false，
// 让调用方知道自己冻结的是一次重放。

// CardDeliveryEvent 是一条待投递的刷卡事件，含原始事件与已冻结的回复。
type CardDeliveryEvent struct {
	EventKey     string `gorm:"column:event_key"`
	EventJSON    []byte `gorm:"column:event_json"`
	ResponseJSON []byte `gorm:"column:response_json"`
}

// cardDeliveryBatch 一次取多少条待投递事件。设备侧会按批拉取，
// 上限与控制层保持一致，避免两边分页口径不同。
const cardDeliveryBatch = 100

// ListPendingCardDeliveries 返回到期待投递的刷卡事件，按事件 id 升序。
func ListPendingCardDeliveries(ctx context.Context, db *gorm.DB) ([]CardDeliveryEvent, error) {
	rows := []CardDeliveryEvent{}
	err := db.WithContext(ctx).Table("card_event_delivery x").
		Select("x.event_key,e.event_json,x.response_json").
		Joins("JOIN device_event e ON e.event_key=x.event_key").
		Where("x.status='pending' AND x.next_attempt_at<=UTC_TIMESTAMP(3)").
		Order("e.id").Limit(cardDeliveryBatch).Find(&rows).Error
	return rows, err
}

// LockCardDelivery 在行锁下取一条投递记录。冻结与读取回复都要先拿它，
// 免得两个并发请求都看到"还没冻结"然后各写一份。
func LockCardDelivery(tx *gorm.DB, eventKey string) (string, error) {
	var stored struct {
		ResponseJSON []byte `gorm:"column:response_json"`
	}
	err := tx.Table("card_event_delivery").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("event_key=?", eventKey).Take(&stored).Error
	return string(stored.ResponseJSON), err
}

// FindDeviceEvent 取设备的原始事件。冻结回复前要核对事件确实存在且事件键对得上。
func FindDeviceEvent(ctx context.Context, db *gorm.DB, eventKey string) ([]byte, error) {
	var row struct {
		EventJSON []byte `gorm:"column:event_json"`
	}
	err := db.WithContext(ctx).Table("device_event").Where("event_key=?", eventKey).Take(&row).Error
	return row.EventJSON, err
}

// FreezeCardResponse 冻结一条事件的设备回复。
//
// 条件带 "response_json 为空"，因此只首写生效：已冻结的事件更新影响 0 行，
// 调用方据此回报 stored:false 而不是谎称保存成功。
func FreezeCardResponse(ctx context.Context, db *gorm.DB, eventKey string, responseJSON string) (int64, error) {
	res := db.WithContext(ctx).Table("card_event_delivery").
		Where("event_key=? AND (response_json IS NULL OR response_json='')", eventKey).
		Update("response_json", responseJSON)
	return res.RowsAffected, res.Error
}

// FindFrozenCardResponse 读回已冻结的回复。
func FindFrozenCardResponse(ctx context.Context, db *gorm.DB, eventKey string) (string, error) {
	var stored string
	err := db.WithContext(ctx).Table("card_event_delivery").Select("response_json").
		Where("event_key=?", eventKey).Take(&stored).Error
	return stored, err
}

// AdvanceCardDelivery 推进投递状态。retry 累加尝试次数并推迟重试，
// 其余情况标记完成。推迟而不是立刻重投，给设备侧留出回话的时间。
func AdvanceCardDelivery(ctx context.Context, db *gorm.DB, eventKey string, retry bool, lastError string, retryAt time.Time) (int64, error) {
	var res *gorm.DB
	if retry {
		res = db.WithContext(ctx).Table("card_event_delivery").Where("event_key=?", eventKey).
			Updates(map[string]any{
				"attempts":        gorm.Expr("attempts+1"),
				"next_attempt_at": retryAt,
				"last_error":      lastError,
			})
	} else {
		res = db.WithContext(ctx).Table("card_event_delivery").Where("event_key=?", eventKey).
			Updates(map[string]any{"status": "done", "last_error": nil})
	}
	return res.RowsAffected, res.Error
}
