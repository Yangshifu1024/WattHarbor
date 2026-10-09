package payment

import (
	"chargepilot/internal/wallet"
	"database/sql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RechargeSources 提供钱包退款所需的原支付来源，不持有连接或退款写入职责。
type RechargeSources struct{}

// LockRechargeSources 在调用方 tx 中当前读并锁 userID 的有效微信充值支付。
// 调用方已持有钱包锁，按支付主键递增加锁；负责提交或回滚。
// 调用方须使用 READ COMMITTED，或在钱包锁后首次建立 RR 快照，以免候选集合遗漏新到账来源。
// 返回不含 ORM 映射的业务投影；无来源返回空集合、nil，错误表示数据库失败。
// 先普通读取已支付候选主键，再按主键集合当前读锁定并复核状态。RR 下带状态筛选的加锁扫描
// 可能同时锁住用户的待支付行，形成退款钱包→待付支付与到账支付→钱包的循环；
// 候选查询不加锁，指定 PRIMARY 索引按候选 ID 加锁，只接触已经提交的已支付来源；
// 到账重放不会再次取得钱包锁。固定两次查询，避免按来源逐条往返数据库。
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
		ID                  uint64         // 原支付主键
		BizID               uint64         // 原充值业务身份
		PaidCents           int64          // 已收金额，分
		TotalCents          int64          // 原申请金额，分
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
