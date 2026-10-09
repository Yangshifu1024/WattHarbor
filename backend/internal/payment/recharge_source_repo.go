package payment

import (
	"chargepilot/internal/wallet"
	"database/sql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RechargeSources 提供钱包退款所需的支付来源，不持有连接或退款写入职责。
type RechargeSources struct{}

// LockRechargeSources 在已锁钱包的事务中，按支付主键递增锁定有效微信充值。
// 事务须为 READ COMMITTED，或在钱包锁后首次建立 RR 快照，避免漏掉刚到账的来源。
// 先不加锁读取已支付候选 ID，再用 PRIMARY 索引按候选 ID 锁定并复核状态。
// 不能直接按支付状态扫描加锁，否则可能锁到待支付记录，与到账事务形成死锁。
// 固定两次查询；调用方负责提交或回滚。
func (RechargeSources) LockRechargeSources(tx *gorm.DB, userID uint64) ([]wallet.RechargeSource, error) {
	var ids []uint64
	if err := tx.Table("payment_order").Where("user_id=? AND biz_type='wallet_recharge' AND pay_method='wechat' AND status IN ('paid','partial_refunded') AND deleted_at IS NULL", userID).Order("id").Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	out := make([]wallet.RechargeSource, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID                  uint64         // 支付主键
		BizID               uint64         // 充值业务编号
		PaidCents           int64          // 已收金额，分
		TotalCents          int64          // 申请金额，分
		RefundedCents       int64          // 成功退款累计，分
		WechatTransactionID sql.NullString // 渠道交易号；空值不可原路退款
	}
	if err := tx.Table("payment_order FORCE INDEX (PRIMARY)").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ? AND user_id=? AND biz_type='wallet_recharge' AND pay_method='wechat' AND status IN ('paid','partial_refunded') AND deleted_at IS NULL", ids, userID).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, wallet.RechargeSource{ID: r.ID, BizID: r.BizID, PaidCents: r.PaidCents, TotalCents: r.TotalCents, RefundedCents: r.RefundedCents, TransactionConfirmed: r.WechatTransactionID.Valid})
	}
	return out, nil
}
