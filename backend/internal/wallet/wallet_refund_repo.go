// Package wallet 实现钱包退款的预占、结算与释放。
// 钱包表（wallet_account / wallet_txn / wallet_refund_part）的写入
// 集中在退款事务内完成；结算用例只传入资金标识与结果，不直接写钱包表。
package wallet

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrWalletRefundConflict 表示钱包退款的身份或金额冲突（预占重复、余额不足等）。
var ErrWalletRefundConflict = errors.New("wallet refund identity or amount conflict")

// Settlement 是退款结算事务传给钱包侧的必要信息。
type Settlement struct {
	ID          uint64 // 退款主键，关联来源分摊
	UserID      uint64 // 支付及钱包所属用户
	RefundNo    string // 退款单号，派生稳定出账流水
	BizType     string // wallet_recharge 处理钱包；其他业务不操作钱包
	RefundCents int64  // 已确认成功或终态失败的本笔金额，分
}

// LockRefundWallet 在回执事务中锁定退款来源钱包，核对用户与分摊金额。
// 非钱包退款无需处理；缺来源或金额不符时报错。调用方须先锁钱包再锁支付，避免死锁。
func LockRefundWallet(tx *gorm.DB, s Settlement) error {
	if s.BizType != "wallet_recharge" {
		return nil
	}
	var part struct {
		WalletAccountID uint64 // 预占钱包主键
		AmountCents     int64  // 分摊预占额，分
	}
	if err := tx.Table("wallet_refund_part").Where("refund_record_id=?", s.ID).Take(&part).Error; err != nil {
		return err
	}
	if part.AmountCents != s.RefundCents {
		return ErrWalletRefundConflict
	}
	var account struct {
		ID uint64 // 被锁定的所属钱包主键
	}
	return tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=? AND deleted_at IS NULL", part.WalletAccountID, s.UserID).Take(&account).Error
}

// SettleRefundWallet 扣除渠道已确认退款的余额与冻结额，并保存退款流水。
// 调用前须锁钱包与支付；金额不符、余额不足或已处理时报冲突，写入失败全部回滚。
// 钱包资金、分摊完成标记与渠道回执同事务，不再次向渠道申请退款。
func SettleRefundWallet(tx *gorm.DB, s Settlement) error {
	if s.BizType != "wallet_recharge" {
		return nil
	}
	var part struct {
		WalletAccountID uint64 // 预占钱包主键
		AmountCents     int64  // 分摊预占额，分
		Settled         bool   // 该分摊是否已结算或终态释放
	}
	// 调用方已经锁钱包，再当前读分摊；初次定位来源的旧 RR 快照不能用于资金结算。
	if err := tx.Table("wallet_refund_part").Clauses(clause.Locking{Strength: "UPDATE"}).Where("refund_record_id=?", s.ID).Take(&part).Error; err != nil {
		return err
	}
	if part.Settled || part.AmountCents != s.RefundCents {
		return ErrWalletRefundConflict
	}
	var w struct {
		BalanceCents int64 // 当前总余额，分
		FrozenCents  int64 // 当前退款预占额，分
	}
	// 已持钱包锁仍需当前读余额，既用于扣款上界，也用于写入真实 balance_after_cents。
	if err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=?", part.WalletAccountID, s.UserID).Take(&w).Error; err != nil {
		return err
	}
	if w.BalanceCents < s.RefundCents || w.FrozenCents < s.RefundCents {
		return ErrWalletRefundConflict
	}
	if err := tx.Table("wallet_account").Where("id=?", part.WalletAccountID).Updates(map[string]any{"balance_cents": gorm.Expr("balance_cents-?", s.RefundCents), "frozen_cents": gorm.Expr("frozen_cents-?", s.RefundCents), "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := tx.Table("wallet_txn").Create(map[string]any{"txn_no": "RF" + s.RefundNo, "user_id": s.UserID, "wallet_account_id": part.WalletAccountID, "direction": "out", "amount_cents": s.RefundCents, "balance_after_cents": w.BalanceCents - s.RefundCents, "biz_type": "refund", "biz_ref": s.RefundNo, "note": "provider refund succeeded", "created_month": time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}).Error; err != nil {
		return err
	}
	return tx.Table("wallet_refund_part").Where("refund_record_id=?", s.ID).Update("settled", true).Error
}

// ReleaseFailedRefund 仅在渠道确认终态失败后释放冻结额，结果未知时保留预占。
// 调用前按钱包→支付锁序取得锁；只减少冻结额并标记分摊已处理，不扣余额。
func ReleaseFailedRefund(tx *gorm.DB, s Settlement) error {
	if s.BizType != "wallet_recharge" {
		return nil
	}
	var part struct {
		WalletAccountID uint64 // 预占钱包主键
		AmountCents     int64  // 分摊预占额，分
		Settled         bool   // 该分摊是否已结算或终态释放
	}
	// 钱包及退款已被调用方串行锁定；当前读取分摊，已释放的预占不能再次释放。
	if err := tx.Table("wallet_refund_part").Clauses(clause.Locking{Strength: "UPDATE"}).Where("refund_record_id=?", s.ID).Take(&part).Error; err != nil {
		return err
	}
	if part.Settled || part.AmountCents != s.RefundCents {
		return ErrWalletRefundConflict
	}
	result := tx.Table("wallet_account").Where("id=? AND user_id=? AND frozen_cents>=?", part.WalletAccountID, s.UserID, s.RefundCents).Updates(map[string]any{"frozen_cents": gorm.Expr("frozen_cents-?", s.RefundCents), "version": gorm.Expr("version+1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrWalletRefundConflict
	}
	return tx.Table("wallet_refund_part").Where("refund_record_id=?", s.ID).Update("settled", true).Error
}
