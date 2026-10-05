package repo

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
	// EventKey 原持久事件幂等键。
	EventKey string `gorm:"column:event_key"`
	// EventJSON 原持久协议事件 JSON 字节。
	EventJSON []byte `gorm:"column:event_json"`
	// ResponseJSON 原首次冻结的设备回复 JSON 字节。
	ResponseJSON []byte `gorm:"column:response_json"`
}

// cardDeliveryBatch 一次取多少条待投递事件。设备侧会按批拉取，
// 上限与控制层保持一致，避免两边分页口径不同。
const cardDeliveryBatch = 100

// ListPendingCardDeliveries 接收 ctx 与 db，按原事件 id 升序读取最多一百条到期待投递卡事件及冻结回复；返回列表和 SQL 错误，无状态推进。
func ListPendingCardDeliveries(ctx context.Context, db *gorm.DB) ([]CardDeliveryEvent, error) {
	rows := []CardDeliveryEvent{}
	err := db.WithContext(ctx).Table("card_event_delivery x").
		Select("x.event_key,e.event_json,x.response_json").
		Joins("JOIN device_event e ON e.event_key=x.event_key").
		Where("x.status='pending' AND x.next_attempt_at<=UTC_TIMESTAMP(3)").
		Order("e.id").Limit(cardDeliveryBatch).Find(&rows).Error
	return rows, err
}

// LockCardDelivery 接收所属 tx 与 eventKey，以 UPDATE 锁读取原投递回复；返回文本或缺失、SQL 错误，锁由调用事务释放。
func LockCardDelivery(tx *gorm.DB, eventKey string) (string, error) {
	var stored struct {
		// ResponseJSON 原首次冻结的设备回复 JSON 字节。
		ResponseJSON []byte `gorm:"column:response_json"`
	}
	err := tx.Table("card_event_delivery").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("event_key=?", eventKey).Take(&stored).Error
	return string(stored.ResponseJSON), err
}

// FindDeviceEvent 接收 ctx、db 与 eventKey，读取原协议事件 JSON；返回字节或缺失、SQL 错误，无写入。
func FindDeviceEvent(ctx context.Context, db *gorm.DB, eventKey string) ([]byte, error) {
	var row struct {
		// EventJSON 原持久协议事件 JSON 字节。
		EventJSON []byte `gorm:"column:event_json"`
	}
	err := db.WithContext(ctx).Table("device_event").Where("event_key=?", eventKey).Take(&row).Error
	return row.EventJSON, err
}

// FreezeCardResponse 接收 ctx、db、eventKey 与 responseJSON，仅对未冻结回复首写；返回影响行数和 SQL 错误，重放零行不覆盖原文。
func FreezeCardResponse(ctx context.Context, db *gorm.DB, eventKey string, responseJSON string) (int64, error) {
	res := db.WithContext(ctx).Table("card_event_delivery").
		Where("event_key=? AND (response_json IS NULL OR response_json='')", eventKey).
		Update("response_json", responseJSON)
	return res.RowsAffected, res.Error
}

// FindFrozenCardResponse 接收 ctx、db 与 eventKey，读取原冻结设备回复；返回文本或缺失、SQL 错误，无写入。
func FindFrozenCardResponse(ctx context.Context, db *gorm.DB, eventKey string) (string, error) {
	var stored string
	err := db.WithContext(ctx).Table("card_event_delivery").Select("response_json").
		Where("event_key=?", eventKey).Take(&stored).Error
	return stored, err
}

// AdvanceCardDelivery 接收 ctx、db、eventKey、retry、lastError 与 retryAt；重试累加原尝试次数并延后，成功标记 done 清除错误；返回影响行数及 SQL 错误，不重建事件身份。
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
