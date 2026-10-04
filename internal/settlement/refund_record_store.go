package settlement

import (
	"gorm.io/gorm"
	"time"
)

// RefundRecordInput 是在调用方事务内创建退款记录所需的内容。
type RefundRecordInput struct {
	RefundNo        string
	PaymentOrderID  uint64
	UserID          uint64
	BizType         string
	BizID           uint64
	RefundCents     int64
	Reason          string
	Status          string // pending 待退款 / success 已退
	ExecutionPolicy string // automatic 自动执行 / manual_review 需人工复核
}

// InsertRefundRecord 写一笔退款。created_month 是分区列，由调用方传入
// 业务发生时间所在月份，不取当前时间——跨月重放要落回原来的月份。
func InsertRefundRecord(tx *gorm.DB, in RefundRecordInput, month time.Time) error {
	return tx.Table("refund_record").Create(map[string]any{
		"execution_policy": in.ExecutionPolicy, "refund_no": in.RefundNo,
		"payment_order_id": in.PaymentOrderID, "user_id": in.UserID,
		"biz_type": in.BizType, "biz_id": in.BizID, "refund_cents": in.RefundCents,
		"reason": in.Reason, "status": in.Status, "created_month": month,
	}).Error
}
