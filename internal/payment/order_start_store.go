package payment

import (
	"database/sql"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 充电支付记录的查询与退款金额更新；调用方负责业务事务和锁顺序。
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
