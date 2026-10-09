package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 刷卡事件由 gateway 持久化，worker 读取事件及已保存回复后投递。
// 回复只保存首次内容，重试继续使用原回复。

// CardDeliveryEvent 保存待处理卡事件及首次保存的设备回复。
type CardDeliveryEvent struct {
	// EventKey 持久事件幂等键。
	EventKey string `gorm:"column:event_key"`
	// EventJSON 持久协议事件 JSON 字节。
	EventJSON []byte `gorm:"column:event_json"`
	// ResponseJSON 首次冻结的设备回复 JSON 字节。
	ResponseJSON []byte `gorm:"column:response_json"`
}

// cardDeliveryBatch 是 worker 单次读取待投递刷卡事件的条数上限。
const cardDeliveryBatch = 100

// ListPendingCardDeliveries 按事件 ID 升序返回最多 100 条到期卡事件及其回复。
func ListPendingCardDeliveries(ctx context.Context, db *gorm.DB) ([]CardDeliveryEvent, error) {
	rows := []CardDeliveryEvent{}
	err := db.WithContext(ctx).Table("card_event_delivery x").
		Select("x.event_key,e.event_json,x.response_json").
		Joins("JOIN device_event e ON e.event_key=x.event_key").
		Where("x.status='pending' AND x.next_attempt_at<=UTC_TIMESTAMP(3)").
		Order("e.id").Limit(cardDeliveryBatch).Find(&rows).Error
	return rows, err
}

// CardDecisionEvidence 返回待验证事件，或已保存的设备回复。
type CardDecisionEvidence struct {
	// RawEvent 是尚未生成回复且已通过校验的事件 JSON。
	RawEvent []byte
	// FrozenResponse 是已保存的回复；有值时不再读取或校验事件。
	FrozenResponse string
}

// ReadCardDecision 锁定投递记录，再读取事件并调用 validate；validate 必须非 nil。
// 已有回复时直接返回，通过回滚结束事务；事件校验也在持锁期间执行。
func ReadCardDecision(
	ctx context.Context,
	db *gorm.DB,
	eventKey string,
	validate func([]byte) error,
) (CardDecisionEvidence, error) {
	var eventJSON string
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		frozen, err := lockCardDelivery(tx, eventKey)
		if err != nil {
			return err
		}
		if frozen != "" {
			return errResponseFrozen(frozen)
		}
		raw, err := findDeviceEvent(ctx, tx, eventKey)
		if err != nil {
			return err
		}
		if err := validate(raw); err != nil {
			return err
		}
		eventJSON = string(raw)
		return nil
	})

	var frozen *frozenResponse
	if errors.As(err, &frozen) {
		return CardDecisionEvidence{FrozenResponse: frozen.response}, nil
	}
	if err != nil {
		return CardDecisionEvidence{}, err
	}
	return CardDecisionEvidence{RawEvent: []byte(eventJSON)}, nil
}

// frozenResponse 携带已有回复，通过返回错误结束查询事务。
type frozenResponse struct {
	// response 已冻结 JSON，沿用存量原文返回。
	response string
}

// Error 返回已有回复的内部回滚提示。
func (e *frozenResponse) Error() string { return "card response already frozen" }

// errResponseFrozen 携带已保存回复，用于触发查询事务回滚。
func errResponseFrozen(response string) error { return &frozenResponse{response: response} }

// ErrCardResponseFreezeFailed 表示条件更新失败，返回错误保留底层原因，不再读取或重试。
var ErrCardResponseFreezeFailed = errors.New("freeze card response failed")

// ErrCardResponseReadFailed 表示首次写入未影响一行后的存量读取失败，返回错误保留底层原因。
var ErrCardResponseReadFailed = errors.New("read frozen card response failed")

// FreezeCardResponse 仅在回复为 NULL 或空串时保存 candidate。
// 成功返回 candidate 和 true；否则读取原回复并返回 false，读取与更新失败分别报错。
func FreezeCardResponse(
	ctx context.Context,
	db *gorm.DB,
	eventKey string,
	candidate string,
) (string, bool, error) {
	stored, err := freezeCardResponse(ctx, db, eventKey, candidate)
	if err != nil {
		return "", false, fmt.Errorf("%w: %w", ErrCardResponseFreezeFailed, err)
	}
	if stored == 1 {
		return candidate, true, nil
	}
	frozen, err := findFrozenCardResponse(ctx, db, eventKey)
	if err != nil {
		return "", false, fmt.Errorf("%w: %w", ErrCardResponseReadFailed, err)
	}
	return frozen, false, nil
}

// lockCardDelivery 使用行锁读取事件的回复，锁持续到调用方事务结束。
func lockCardDelivery(tx *gorm.DB, eventKey string) (string, error) {
	var stored struct {
		// ResponseJSON 首次冻结的设备回复 JSON 字节。
		ResponseJSON []byte `gorm:"column:response_json"`
	}
	err := tx.Table("card_event_delivery").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("event_key=?", eventKey).Take(&stored).Error
	return string(stored.ResponseJSON), err
}

// findDeviceEvent 按事件编号读取协议事件 JSON。
func findDeviceEvent(ctx context.Context, db *gorm.DB, eventKey string) ([]byte, error) {
	var row struct {
		// EventJSON 持久协议事件 JSON 字节。
		EventJSON []byte `gorm:"column:event_json"`
	}
	err := db.WithContext(ctx).Table("device_event").Where("event_key=?", eventKey).Take(&row).Error
	return row.EventJSON, err
}

// freezeCardResponse 仅更新尚未保存的回复，返回影响行数。
func freezeCardResponse(ctx context.Context, db *gorm.DB, eventKey string, responseJSON string) (int64, error) {
	res := db.WithContext(ctx).Table("card_event_delivery").
		Where("event_key=? AND (response_json IS NULL OR response_json='')", eventKey).
		Update("response_json", responseJSON)
	return res.RowsAffected, res.Error
}

// findFrozenCardResponse 读取已保存的设备回复。
func findFrozenCardResponse(ctx context.Context, db *gorm.DB, eventKey string) (string, error) {
	var stored string
	err := db.WithContext(ctx).Table("card_event_delivery").Select("response_json").
		Where("event_key=?", eventKey).Take(&stored).Error
	return stored, err
}

// AdvanceCardDelivery 重试时增加次数并延后执行，成功时标记 done 并清除错误；返回影响行数。
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
