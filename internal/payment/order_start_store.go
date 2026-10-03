package payment

import (
	"database/sql"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 起充结果回执的落库原语：卡操作状态、订单与支付单的回退、以及两张
// 退款家族表。这些写入在 gateway 回执接口的事务里，必须与订单状态
// 一起提交——半途失败会留下"钱已退但订单仍在充电"的状态。
//
// 锁顺序固定：钱包 → card_charge（仅占锁）→ 订单 → 支付单。
// 换顺序会和别处的加锁顺序形成环，表现为偶发死锁。

// OrderPaymentSummary 是一笔支付单在起充失败时要核对的关键金额。
type OrderPaymentSummary struct {
	ID                  uint64
	UserID              uint64
	TotalCents          int64
	PaidCents           int64
	RefundedCents       int64
	WechatTransactionID sql.NullString
	Status              string
}

// FindOrderPaymentForCharge 按订单取其支付单的支付方式与归属。
// balance 走钱包退款，wechat 走渠道退款，两条路的核对口径不同。
func FindOrderPaymentForCharge(tx *gorm.DB, chargeOrderID uint64) (string, uint64, error) {
	var row struct {
		PayMethod string
		UserID    uint64
	}
	err := tx.Table("payment_order p").Select("p.pay_method,p.user_id").
		Joins("JOIN charge_order c ON c.payment_order_id=p.id").
		Where("c.id=?", chargeOrderID).Take(&row).Error
	return row.PayMethod, row.UserID, err
}

// LockCardChargeForOrder 只对 card_charge 取行锁参与加锁顺序，不读任何字段。
// 这样起充回执与刷卡会话共用同一把锁，避免两处同时改同一笔卡的占用端口。
func LockCardChargeForOrder(tx *gorm.DB, chargeOrderID uint64) error {
	var locked struct{ ID uint64 }
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("card_charge").
		Where("charge_order_id=?", chargeOrderID).Find(&locked).Error
}

// ConfirmCardStartOperation 把等待确认的刷卡起充操作标记为已确认。
// 只有刷卡起充才有这张表的行；扫码与余额起充影响 0 行是正常结果。
// 返回值供调用方按需判断，调用方不得把 0 行当冲突——判据是 err，不是 RowsAffected。
func ConfirmCardStartOperation(tx *gorm.DB, chargeOrderID uint64, at time.Time) (int64, error) {
	res := tx.Table("card_operation").
		Where("charge_order_id=? AND kind='start' AND status='confirming'", chargeOrderID).
		Updates(map[string]any{"status": "confirmed", "confirmed_at": at})
	return res.RowsAffected, res.Error
}

// FailCardStartOperation 把该订单的刷卡起乘操作标记为失败。
func FailCardStartOperation(tx *gorm.DB, chargeOrderID uint64, reason string, at time.Time) error {
	return tx.Table("card_operation").Where("charge_order_id=? AND kind='start'", chargeOrderID).
		Updates(map[string]any{"status": "failed", "failure_reason": reason, "confirmed_at": at}).Error
}

// ClearCardActivePort 释放卡在这笔订单上占用的端口。
func ClearCardActivePort(tx *gorm.DB, chargeOrderID uint64) error {
	return tx.Table("card_charge").Where("charge_order_id=?", chargeOrderID).
		Update("active_port", nil).Error
}

// ReadBalancePaymentAmounts 返回余额支付单的实付与已退金额，
// 用于退钱包前核对可退额度。
func ReadBalancePaymentAmounts(tx *gorm.DB, paymentOrderID uint64) (paid, refunded int64, err error) {
	var row struct {
		PaidCents     int64
		RefundedCents int64
	}
	err = tx.Table("payment_order").Select("paid_cents,refunded_cents").
		Where("id=? AND pay_method='balance'", paymentOrderID).Take(&row).Error
	return row.PaidCents, row.RefundedCents, err
}

// LockWechatPaymentForRefund 在行锁下取订单的微信支付单，并核对它确实属于
// 这笔订单：biz_type、pay_method、biz_id、user_id 四项都要对上，且已实付。
// 任何一项不符都说明支付单与订单脱节，此时不能凭它发起退款。
func LockWechatPaymentForRefund(tx *gorm.DB, chargeOrderID uint64) (OrderPaymentSummary, error) {
	var row OrderPaymentSummary
	err := tx.Table("charge_order AS c").
		Select("p.id, p.user_id, p.total_cents, p.paid_cents, p.refunded_cents, p.wechat_transaction_id, p.status").
		Joins("JOIN payment_order AS p ON p.id = c.payment_order_id AND p.biz_type = 'charge' "+
			"AND p.pay_method = 'wechat' AND p.biz_id = c.id AND p.user_id = c.user_id").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("c.id = ? AND p.status = 'paid' AND p.deleted_at IS NULL", chargeOrderID).Take(&row).Error
	return row, err
}

// AddPaymentRefunded 把支付单的已退金额累加并推进状态。
// 状态由调用方按"退完没有"决定，不在这里判断：这里的判断要基于刚才读到的
// 同一份金额，隔一次查询就可能读到别人改过的值。
func AddPaymentRefunded(tx *gorm.DB, paymentID uint64, cents int64, status string) error {
	return tx.Table("payment_order").Where("id=?", paymentID).
		Updates(map[string]any{"refunded_cents": gorm.Expr("refunded_cents+?", cents), "status": status}).Error
}

// RefundRecordInput 是一笔退款的写入内容。refund_record 归属 refund 家族，
// order 侧要用它时经这里写入，避免反向依赖。
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

// SetOrderStatusRefunded 把订单标记为已退款。起充失败且钱已退回时使用。
func SetOrderStatusRefunded(tx *gorm.DB, orderID uint64) error {
	return tx.Model(&orderstore.ChargeOrderRecord{}).Where("id=?", orderID).Update("status", "refunded").Error
}

// EnqueueChargeBilling 把结束计量排队。计费是异步的：结束回执只负责确认
// 事实并排队，费用由 worker 算完再落账单。
func EnqueueChargeBilling(tx *gorm.DB, chargeOrderID uint64) error {
	return tx.Table("charge_billing_job").Create(map[string]any{"charge_order_id": chargeOrderID}).Error
}
