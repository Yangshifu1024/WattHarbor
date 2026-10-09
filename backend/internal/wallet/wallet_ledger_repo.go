// 钱包账本原语：行锁读取与余额变动流水。
// 只触及 wallet_account / wallet_txn 两表，供刷卡、结算、启动失败退款等
// 跨家族事务复用；错误语义以本包哨兵表达，调用方按业务映射。
package wallet

import (
	"errors"
	"math"

	"chargepilot/internal/platform/dbutil"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrInactive 表示对非 active 状态钱包出账。
var ErrInactive = errors.New("wallet account is not active for debit")

// ErrInsufficient 表示钱包可用余额（balance - frozen）不足。
var ErrInsufficient = errors.New("wallet spendable balance insufficient")

// ErrInvalidBalance 表示余额、冻结额或变动金额不满足账本范围，调用方必须回滚事务。
var ErrInvalidBalance = errors.New("invalid wallet balance or movement")

// Row 是 wallet_account 的行锁投影。
type Row struct {
	ID           uint64 // 已锁钱包主键
	BalanceCents int64  // 当前账面余额，分
	FrozenCents  int64  // 当前冻结金额，分，不能出账使用
	Status       string // active 可消费，其他状态拒绝出账
}

// Lock 在调用方 tx 中以 UPDATE 锁读取 user 的未删除账户，返回行投影或读取错误。
// 缺账户返回 gorm.ErrRecordNotFound；调用方须持有钱包锁后再锁支付，并负责提交或回滚。
func Lock(tx *gorm.DB, user uint64) (Row, error) {
	var w Row
	err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND deleted_at IS NULL", user).Take(&w).Error
	return w, err
}

// Move 变动钱包余额并记录 wallet_txn 流水：cents 为负出账、正入账，零值不动。
// 出账要求 active 状态且可用余额充足，违约返回 ErrInactive / ErrInsufficient。
// tx 须已持有 user 的 w 钱包锁，ref 为稳定流水及业务身份，note 为原业务说明。
// 返回写入错误要求整体回滚；w 的余额投影会随操作更新，失败后不能用于再次提交。
func Move(tx *gorm.DB, user uint64, w *Row, cents int64, ref, note string) error {
	if w == nil || w.BalanceCents < 0 || w.FrozenCents < 0 || w.FrozenCents > w.BalanceCents || cents == math.MinInt64 {
		return ErrInvalidBalance
	}
	if cents > 0 && w.BalanceCents > math.MaxInt64-cents {
		return ErrInvalidBalance
	}
	if cents < 0 && w.Status != "active" {
		return ErrInactive
	}
	if cents < 0 && w.BalanceCents-w.FrozenCents < -cents {
		return ErrInsufficient
	}
	if cents == 0 {
		return nil
	}
	w.BalanceCents += cents
	direction, biz := "in", "refund"
	amount := cents
	if cents < 0 {
		direction, biz, amount = "out", "pay", -cents
	}
	if err := tx.Table("wallet_account").Where("id=?", w.ID).Updates(map[string]any{"balance_cents": w.BalanceCents, "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	return tx.Table("wallet_txn").Create(map[string]any{
		"txn_no": ref, "user_id": user, "wallet_account_id": w.ID,
		"direction": direction, "amount_cents": amount,
		"balance_after_cents": w.BalanceCents, "biz_type": biz,
		"biz_ref": ref, "note": note, "created_month": dbutil.MonthStart(),
	}).Error
}
