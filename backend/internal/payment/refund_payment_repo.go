package payment

import (
	"database/sql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RefundPayment 是退款需核对的支付信息，映射与状态写入由 payment 负责。
type RefundPayment struct {
	ID                  uint64         // 支付主键
	OrderNo             string         // 渠道商户单号
	UserID              uint64         // 付款用户
	BizID               uint64         // 业务主键，充值可为 0
	BizType             string         // charge 或 wallet_recharge
	PayMethod           string         // wechat 或 balance
	WechatTransactionID sql.NullString // 渠道交易号；Valid=false 时不能发起渠道退款
	TotalCents          int64          // 应付总额，分
	PaidCents           int64          // 已确认到账金额，分
	RefundedCents       int64          // 已确认渠道退回累计金额，分
	Status              string         // 支付状态；申请退款不等于退款成功
}

// LockPaymentForRefund 在调用方事务中锁 id 的未删除支付信息，缺行或读取失败返回错误。
// 调用方按钱包→支付→退款顺序持锁并负责提交，不在此修改状态。
func LockPaymentForRefund(tx *gorm.DB, id uint64) (RefundPayment, error) {
	var out RefundPayment
	err := tx.Table("payment_order").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&out).Error
	return out, err
}

// LockPaymentForRefundReceipt 锁 id 的支付，包括后来软删除的记录，以恢复已确认渠道回执。
// 返回当前支付记录或读取错误；tx 由调用方提交，不请求渠道。
func LockPaymentForRefundReceipt(tx *gorm.DB, id uint64) (RefundPayment, error) {
	var out RefundPayment
	err := tx.Table("payment_order").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&out).Error
	return out, err
}

// SetConfirmedRefundInTx 在已锁支付的事务中将 id 的确定退款累计额设为 cents（分）及 status。
// 调用方已校验回执、实付上界和状态；返回数据库错误，不改变其他模块状态或提交事务。
func SetConfirmedRefundInTx(tx *gorm.DB, id uint64, cents int64, status string) error {
	return tx.Table("payment_order").Where("id=?", id).Updates(map[string]any{"refunded_cents": cents, "status": status}).Error
}
