package payment

import (
	"chargepilot/internal/domain/money"
	orderdomain "chargepilot/internal/domain/order"
	"database/sql"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 充电支付记录的查询与退款金额更新；调用方负责业务事务和锁顺序。
//
// 锁顺序固定：钱包 → card_charge（仅占锁）→ 订单 → 支付单。
// 这是启动回执调用路径的锁序；本文件不自行锁定钱包或卡会话。

// OrderPaymentSummary 是一笔支付单在起充失败时要核对的关键金额。
type OrderPaymentSummary struct {
	ID                  uint64              // 支付主键
	UserID              uint64              // 付款用户，须匹配订单
	TotalCents          int64               // 申请金额，分
	PaidCents           int64               // 已确认实付，分
	RefundedCents       int64               // 已成功退回累计，分
	WechatTransactionID sql.NullString      // 渠道交易号，未确认为空
	Status              money.PaymentStatus // 支付状态，锁定读取只接受 paid
}

// FindOrderPaymentForCharge 在事务中按 chargeOrderID 读取关联支付的方式与用户 ID，返回两项及读取错误。
// 缺关联返回 gorm.ErrRecordNotFound，不写入；调用方按返回的 money.PayMethod 选择退款路径并负责事务。
func FindOrderPaymentForCharge(tx *gorm.DB, chargeOrderID uint64) (money.PayMethod, uint64, error) {
	var row struct {
		PayMethod money.PayMethod // 支付方式，取自 payment_order.pay_method 词表
		UserID    uint64          // 付款用户
	}
	err := tx.Table("payment_order p").Select("p.pay_method,p.user_id").
		Joins("JOIN charge_order c ON c.payment_order_id=p.id").
		Where("c.id=?", chargeOrderID).Take(&row).Error
	return row.PayMethod, row.UserID, err
}

// ReadBalancePaymentAmounts 在调用方事务中读取 paymentOrderID 余额支付的 paid 实付及 refunded 已退金额（分）。
// 调用方须已锁钱包及支付以核对剩余额度；缺单返回 err，不更新资金或提交。
func ReadBalancePaymentAmounts(tx *gorm.DB, paymentOrderID uint64) (paid, refunded int64, err error) {
	var row struct {
		PaidCents     int64 // 已确认实付，分
		RefundedCents int64 // 已成功退回累计，分
	}
	err = tx.Table("payment_order").Select("paid_cents,refunded_cents").
		Where("id=? AND pay_method=?", paymentOrderID, money.PayMethodBalance).Take(&row).Error
	return row.PaidCents, row.RefundedCents, err
}

// LockWechatPaymentForRefund 在行锁下取订单的微信支付单，并核对它确实属于
// 这笔订单：biz_type、pay_method、biz_id、user_id 四项都要对上，且已实付。
// 任何一项不符都说明支付单与订单脱节，此时不能凭它发起退款。
// tx 由调用方持有并提交，chargeOrderID 为订单；返回锁定金额查询结果或缺单、读取错误，不更新退款。
func LockWechatPaymentForRefund(tx *gorm.DB, chargeOrderID uint64) (OrderPaymentSummary, error) {
	var row OrderPaymentSummary
	err := tx.Table("charge_order AS c").
		Select("p.id, p.user_id, p.total_cents, p.paid_cents, p.refunded_cents, p.wechat_transaction_id, p.status").
		Joins("JOIN payment_order AS p ON p.id = c.payment_order_id AND p.biz_type = 'charge' "+
			"AND p.pay_method = ? AND p.biz_id = c.id AND p.user_id = c.user_id", money.PayMethodWechat).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("c.id = ? AND p.status = ? AND p.deleted_at IS NULL", chargeOrderID, orderdomain.StatusPaid).Take(&row).Error
	return row, err
}

// AddPaymentRefunded 把支付单的已退金额累加并更新状态。
// 状态由调用方依据持锁期间读取的同一份支付金额决定。
// tx 须已持有 paymentID 支付锁，cents 为本次确认退款的金额（分），status 为核对后的目标状态。
// 返回更新错误要求业务、钱包及退款一起回滚；不自行提交或请求渠道。
func AddPaymentRefunded(tx *gorm.DB, paymentID uint64, cents int64, status money.PaymentStatus) error {
	return tx.Table("payment_order").Where("id=?", paymentID).
		Updates(map[string]any{"refunded_cents": gorm.Expr("refunded_cents+?", cents), "status": status}).Error
}
