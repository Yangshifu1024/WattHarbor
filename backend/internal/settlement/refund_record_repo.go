package settlement

import (
	"chargepilot/internal/domain/money"
	"gorm.io/gorm"
	"time"
)

// RefundRecordInput 是在调用方事务内创建退款记录所需的内容。
type RefundRecordInput struct {
	RefundNo        string                // 退款单号，重放及渠道重试沿用
	PaymentOrderID  uint64                // 支付主键
	UserID          uint64                // 付款用户
	BizType         money.PaymentBizType  // charge 或 wallet_recharge
	BizID           uint64                // 业务 ID
	RefundCents     int64                 // 本笔金额，分
	Reason          string                // 退款业务依据
	Status          money.RefundStatus    // pending 待退款 / success 已退
	ExecutionPolicy money.ExecutionPolicy // automatic 自动执行 / manual_review 需人工复核
}

// InsertRefundRecord 在调用方资金事务内保存退款记录，失败须整体回滚。
// created_month 取业务发生月份的 UTC 月首，跨月重放不能改用当前月份。
func InsertRefundRecord(tx *gorm.DB, in RefundRecordInput, month time.Time) error {
	return tx.Table("refund_record").Create(map[string]any{
		"execution_policy": in.ExecutionPolicy, "refund_no": in.RefundNo,
		"payment_order_id": in.PaymentOrderID, "user_id": in.UserID,
		"biz_type": in.BizType, "biz_id": in.BizID, "refund_cents": in.RefundCents,
		"reason": in.Reason, "status": in.Status, "created_month": month,
	}).Error
}
