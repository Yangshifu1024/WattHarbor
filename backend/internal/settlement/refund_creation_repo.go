package settlement

import (
	"chargepilot/internal/domain/money"
	"chargepilot/internal/ops"
	"database/sql"
	"encoding/json"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RefundRecorder 写入 settlement 所属退款记录；不持有连接，使用调用方原事务。
type RefundRecorder struct{}

// RecordLateInTx 在 tx 中创建 in 的迟到支付自动退款和 eventID 对应的 Outbox。
// tx 已持有支付及意图锁，调用方提交或回滚；输入金额为分、月份为 UTC 月首。
// 返回创建或序列化错误，nil 表示待执行凭据已落入同一事务，尚未渠道退款。
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

// CreateInTx 创建 in 对应的 automatic/pending 退款，返回持久主键及错误。
// tx 须已锁钱包（适用时）及原支付，调用方负责额度核对和提交；不能自动重试或换单号。
// 错误要求回滚；成功仅表示退款被登记，钱包预占由 wallet 在相同事务中完成。
func (RefundRecorder) CreateInTx(tx *gorm.DB, in money.RefundDraft) (uint64, error) {
	r := RefundRecord{RefundNo: in.RefundNo, PaymentOrderID: in.PaymentOrderID, UserID: in.UserID, BizType: in.BizType, BizID: in.BizID, RefundCents: in.AmountCents, Reason: sql.NullString{String: in.Reason, Valid: true}, Status: "pending", ExecutionPolicy: "automatic", CreatedMonth: in.CreatedMonth, NextAttemptAt: in.NextAttemptAt}
	if err := tx.Create(&r).Error; err != nil {
		return 0, err
	}
	return r.ID, nil
}

// ReservedInTx 当前读 paymentID 的在途退款聚合（分），不依赖 RR 的旧快照。
// tx 必须先锁原支付行；返回 0、nil 表示没有预占，错误表示读取失败。
// 调用方持有支付锁期间不得出现绕过该锁的退款创建路径。
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
