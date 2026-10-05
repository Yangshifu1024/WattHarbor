package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 刷卡事件由 gateway 持久化，worker 读取原事件及冻结回复后投递。
// 回复按原条件首写，已有回复不覆盖，重放继续使用原冻结值。

// CardDeliveryEvent 是一条待投递的刷卡事件，含原始事件与已冻结的回复。
type CardDeliveryEvent struct {
	// EventKey 原持久事件幂等键。
	EventKey string `gorm:"column:event_key"`
	// EventJSON 原持久协议事件 JSON 字节。
	EventJSON []byte `gorm:"column:event_json"`
	// ResponseJSON 原首次冻结的设备回复 JSON 字节。
	ResponseJSON []byte `gorm:"column:response_json"`
}

// cardDeliveryBatch 是 worker 单次读取待投递刷卡事件的原条数上限。
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

// CardDecisionEvidence 是原刷卡决策输入的持久投影，只携带原事件或已有冻结回复，不解释协议与 UUID。
type CardDecisionEvidence struct {
	// RawEvent 未冻结且校验成功的原事件 JSON 字节，已有冻结回复时不读取事件。
	RawEvent []byte
	// FrozenResponse 原已冻结回复文本，非空时事务沿用正常回滚出口释放锁。
	FrozenResponse string
}

// ReadCardDecision 接收 ctx、db、eventKey 与非 nil validate，在原事务内先锁投递行，再按需读取事件和调用验证。
// 有冻结回复时仍通过原私有错误回滚，在事务外转为正常投影；未冻结时验证成功才提交并返回原事件字节。
// 返回原 SQL/提交/验证错误，验证回调仍在持锁期间执行；不创建新身份、重试或解释设备协议。
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

// frozenResponse 是发现已有冻结回复时的私有正常回滚出口，事务外转为持久投影。
type frozenResponse struct {
	// response 原已冻结 JSON，沿用存量原文返回。
	response string
}

// Error 返回该私有冻结出口的原固定提示，无 IO。
func (e *frozenResponse) Error() string { return "card response already frozen" }

// errResponseFrozen 接收 response 原 JSON，返回携带存量原文的私有错误以触发事务回滚；无 IO。
func errResponseFrozen(response string) error { return &frozenResponse{response: response} }

// ErrCardResponseFreezeFailed 表示原条件更新失败，返回错误保留底层原因，不再读取或重试。
var ErrCardResponseFreezeFailed = errors.New("freeze card response failed")

// ErrCardResponseReadFailed 表示首写未影响一行后的原存量读取失败，返回错误保留底层原因。
var ErrCardResponseReadFailed = errors.New("read frozen card response failed")

// FreezeCardResponse 接收 ctx、db、eventKey 与 candidate，仅按原 NULL/空串条件首写回复。
// 恰更新一行返回 candidate 和 true；否则只读存量并返回原文和 false，更新/读取错误按阶段哨兵区分。
// 不增加外层事务、锁、身份校验、非空检查或重试，未知更新结果不继续读取或再写。
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

// lockCardDelivery 接收所属 tx 与 eventKey，以 UPDATE 锁读取原投递回复；返回文本或缺失、SQL 错误，锁由调用事务释放。
func lockCardDelivery(tx *gorm.DB, eventKey string) (string, error) {
	var stored struct {
		// ResponseJSON 原首次冻结的设备回复 JSON 字节。
		ResponseJSON []byte `gorm:"column:response_json"`
	}
	err := tx.Table("card_event_delivery").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("event_key=?", eventKey).Take(&stored).Error
	return string(stored.ResponseJSON), err
}

// findDeviceEvent 接收 ctx、db 与 eventKey，读取原协议事件 JSON；返回字节或缺失、SQL 错误，无写入。
func findDeviceEvent(ctx context.Context, db *gorm.DB, eventKey string) ([]byte, error) {
	var row struct {
		// EventJSON 原持久协议事件 JSON 字节。
		EventJSON []byte `gorm:"column:event_json"`
	}
	err := db.WithContext(ctx).Table("device_event").Where("event_key=?", eventKey).Take(&row).Error
	return row.EventJSON, err
}

// freezeCardResponse 接收 ctx、db、eventKey 与 responseJSON，仅对未冻结回复首写；返回影响行数和 SQL 错误，重放零行不覆盖原文。
func freezeCardResponse(ctx context.Context, db *gorm.DB, eventKey string, responseJSON string) (int64, error) {
	res := db.WithContext(ctx).Table("card_event_delivery").
		Where("event_key=? AND (response_json IS NULL OR response_json='')", eventKey).
		Update("response_json", responseJSON)
	return res.RowsAffected, res.Error
}

// findFrozenCardResponse 接收 ctx、db 与 eventKey，读取原冻结设备回复；返回文本或缺失、SQL 错误，无写入。
func findFrozenCardResponse(ctx context.Context, db *gorm.DB, eventKey string) (string, error) {
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
