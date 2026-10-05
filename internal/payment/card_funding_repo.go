package payment

import (
	"errors"
	"gorm.io/gorm"
)

// ErrCardFundingConflict 表示卡续购没有匹配到原已付余额支付单。
var ErrCardFundingConflict = errors.New("card funding source conflict")

// CreateCardPaymentInTx 在原 tx 中写入 in 已付余额支付单并回填主键；返回存储错误。
// 金额和卡准入由 card 冻结，与订单、钱包及原操作一起提交，不调用支付渠道。
func CreateCardPaymentInTx(tx *gorm.DB, in *PaymentOrderRecord) error { return tx.Create(in).Error }

// CreateCardIntentInTx 在原 tx 中保存 in 卡支付意图及完整方案，返回唯一键或存储错误。
// 由 card 首次事务提供原身份，不重算历史价格、不提交事务。
func CreateCardIntentInTx(tx *gorm.DB, in *PaymentIntentRecord) error { return tx.Create(in).Error }

// SetCardOrderInTx 在原 tx 中为 paymentID 回填 chargeOrderID 业务关联，返回存储错误。
// 不改变实付金额或到账状态，不提交调用方事务。
func SetCardOrderInTx(tx *gorm.DB, paymentID, chargeOrderID uint64) error {
	return tx.Model(&PaymentOrderRecord{}).Where("id=?", paymentID).Update("biz_id", chargeOrderID).Error
}

// ExtendCardPaymentInTx 在原 tx 中为 id 已付余额单累计 cents 实付和应付金额，单位分。
// 返回原行不匹配的 ErrCardFundingConflict 或存储错误；钱包实际扣款与本写入由 card 原子提交。
func ExtendCardPaymentInTx(tx *gorm.DB, id uint64, cents int64) error {
	res := tx.Model(&PaymentOrderRecord{}).Where("id=? AND pay_method='balance' AND status IN ('paid','partial_refunded')", id).Updates(map[string]any{"paid_cents": gorm.Expr("paid_cents+?", cents), "total_cents": gorm.Expr("total_cents+?", cents)})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrCardFundingConflict
	}
	return nil
}
