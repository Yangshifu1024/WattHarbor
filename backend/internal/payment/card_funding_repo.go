package payment

import (
	"errors"
	"gorm.io/gorm"
)

// ErrCardFundingConflict 表示卡续购没有匹配到已付余额支付单。
var ErrCardFundingConflict = errors.New("card funding source conflict")

// CreateCardPaymentInTx 在调用方事务中写入 in 已付余额支付单并回填主键。
// 金额和卡校验由 card 冻结，与订单、钱包及首次操作一起提交，不调用支付渠道。
func CreateCardPaymentInTx(tx *gorm.DB, in *PaymentOrderRecord) error { return tx.Create(in).Error }

// CreateCardIntentInTx 在调用方事务中保存 in 刷卡支付申请及完整方案，返回写入错误。
// 由 card 首次事务提供标识，不重算历史价格、不提交事务。
func CreateCardIntentInTx(tx *gorm.DB, in *PaymentIntentRecord) error { return tx.Create(in).Error }

// SetCardOrderInTx 在调用方事务中为 paymentID 回填 chargeOrderID 业务关联。
// 不改变实付金额或到账状态，不提交调用方事务。
func SetCardOrderInTx(tx *gorm.DB, paymentID, chargeOrderID uint64) error {
	return tx.Model(&PaymentOrderRecord{}).Where("id=?", paymentID).Update("biz_id", chargeOrderID).Error
}

// ExtendCardPaymentInTx 在调用方事务中为 id 已付余额单累计 cents 实付和应付金额，单位分。
// 未匹配支付记录时返回 ErrCardFundingConflict；钱包实际扣款与本写入由 card 原子提交。
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
