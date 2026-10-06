package payment

import (
	"database/sql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RefundPayment 是退款需核对的原支付事实，映射与状态写入由 payment 拥有。
type RefundPayment struct {
	ID                  uint64         // 原支付主键
	OrderNo             string         // 原渠道商户单号
	UserID              uint64         // 原付款用户
	BizID               uint64         // 原业务主键，充值可为 0
	BizType             string         // charge 或 wallet_recharge
	PayMethod           string         // wechat 或 balance
	WechatTransactionID sql.NullString // 原渠道交易号；Valid=false 时不能发起渠道退款
	TotalCents          int64          // 原应付总额，分
	PaidCents           int64          // 已确认到账金额，分
	RefundedCents       int64          // 已确认渠道退回累计金额，分
	Status              string         // 原支付状态，待退款不提前修改到账事实
}

// LockPaymentForRefund 在调用方 tx 中锁 id 的未删除支付事实，缺行或读取失败返回错误。
// 调用方按原钱包→支付→退款顺序持锁并负责提交，不在此修改状态。
func LockPaymentForRefund(tx *gorm.DB, id uint64) (RefundPayment, error) {
	var out RefundPayment
	err := tx.Table("payment_order").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&out).Error
	return out, err
}

// LockPaymentForRefundReceipt 锁 id 的原支付，包括后来软删除的记录，以恢复已确认渠道回执。
// tx 属于调用方完整回执事务；返回当前事实或错误，不请求渠道或提交事务。
func LockPaymentForRefundReceipt(tx *gorm.DB, id uint64) (RefundPayment, error) {
	var out RefundPayment
	err := tx.Table("payment_order").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&out).Error
	return out, err
}

// SetConfirmedRefundInTx 在已锁支付的 tx 中将 id 的确定退款累计额设为 cents（分）及 status。
// 调用方已校验回执、实付上界和原状态；返回数据库错误，不改变其他模块状态或提交事务。
func SetConfirmedRefundInTx(tx *gorm.DB, id uint64, cents int64, status string) error {
	return tx.Table("payment_order").Where("id=?", id).Updates(map[string]any{"refunded_cents": cents, "status": status}).Error
}
