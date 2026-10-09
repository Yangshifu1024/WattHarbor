package settlement

import (
	"chargepilot/internal/domain/money"
	"chargepilot/internal/ops"
	"database/sql"
	"encoding/json"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RefundRecorder 在调用方事务中保存结算退款记录。
type RefundRecorder struct{}

// RecordLateInTx 在已锁支付和意图的事务中登记迟到支付退款及 Outbox。
// 金额单位为分，月份为 UTC 月首；成功只表示待执行记录已保存。
func (RefundRecorder) RecordLateInTx(tx *gorm.DB, in money.RefundDraft, eventID string) error {
	if _, err := (RefundRecorder{}).CreateInTx(tx, in); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{"event_id": eventID, "refund_no": in.RefundNo, "payment_order_id": in.PaymentOrderID, "amount_cents": in.AmountCents})
	if err != nil {
		return err
	}
	return ops.AppendEventInTx(tx, ops.OutboxEvent{EventID: eventID, Stream: "refund_required_stream", EnvelopeJSON: payload})
}

// CreateInTx 登记 automatic/pending 退款并返回主键。
// 调用方先锁钱包（适用时）和支付，核对额度；钱包预占在同一事务完成，错误整体回滚。
func (RefundRecorder) CreateInTx(tx *gorm.DB, in money.RefundDraft) (uint64, error) {
	r := RefundRecord{RefundNo: in.RefundNo, PaymentOrderID: in.PaymentOrderID, UserID: in.UserID, BizType: in.BizType, BizID: in.BizID, RefundCents: in.AmountCents, Reason: sql.NullString{String: in.Reason, Valid: true}, Status: "pending", ExecutionPolicy: "automatic", CreatedMonth: in.CreatedMonth, NextAttemptAt: in.NextAttemptAt}
	if err := tx.Create(&r).Error; err != nil {
		return 0, err
	}
	return r.ID, nil
}

// ReservedInTx 在支付锁内用当前读合计在途退款，金额单位为分。
// 所有退款创建路径都须遵守该支付锁，避免旧 RR 快照遗漏预占。
func (RefundRecorder) ReservedInTx(tx *gorm.DB, paymentID uint64) (int64, error) {
	var rows []struct {
		RefundCents int64 `gorm:"column:refund_cents"` // 当前在途退款分值，在已锁支付下求和
	}
	if err := tx.Table("refund_record").Clauses(clause.Locking{Strength: "UPDATE"}).Where("payment_order_id=? AND status IN ('pending','processing') AND deleted_at IS NULL", paymentID).Find(&rows).Error; err != nil {
		return 0, err
	}
	var total int64
	for _, r := range rows {
		total += r.RefundCents
	}
	return total, nil
}
