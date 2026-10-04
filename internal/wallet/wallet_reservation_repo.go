package wallet

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WalletRefundReservation 在调用方事务内指定原申请及原路退款金额，冻结钱包并按原充值拆单。
type WalletRefundReservation struct {
	RequestID   string // 原申请 UUID，派生稳定分笔退款号
	UserID      uint64 // 钱包及原充值所属用户
	AmountCents int64  // 本次需预占金额，分，必须大于 0
	Reason      string // 原退款或风险批准依据
}

// refundWalletReservation 是预占时持有行锁的钱包事实。
type refundWalletReservation struct {
	ID           uint64 // 所属钱包主键
	UserID       uint64 // 账户所属用户
	Version      uint64 // 当前版本，资金更新推进版本
	BalanceCents int64  // 账面余额，分
	FrozenCents  int64  // 已预占金额，分
	Status       string // active 或 frozen；已批准风险可在冻结账户继续预占
}

// ReserveInTx 按 req 的原充值来源拆单，并在 tx 内冻结钱包可用余额。
// tx 由调用方提交，锁序为钱包→支付→在途退款；金额单位分，同请求不能二次预占。
// 来源候选查询使用调用方快照：事务须为 READ COMMITTED，或在取得钱包锁后才首次建立 RR 快照。
// nil 表示冻结、原退款及来源分摊均已写入同一事务；错误要求全部回滚。
// 渠道未知结果保留原退款身份与 frozen，不能在本方法中宣称已到账。
func (s RefundService) ReserveInTx(tx *gorm.DB, req WalletRefundReservation) error {
	if s.Sources == nil || s.Recorder == nil {
		return errors.New("wallet refund dependencies not configured")
	}
	var w refundWalletReservation
	if err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND deleted_at IS NULL", req.UserID).Take(&w).Error; err != nil {
		return err
	}
	if req.AmountCents <= 0 || w.BalanceCents < w.FrozenCents || req.AmountCents > w.BalanceCents-w.FrozenCents {
		return ErrWalletRefundConflict
	}
	var existing int64
	if err := tx.Table("wallet_refund_part").Where("request_id=?", req.RequestID).Count(&existing).Error; err != nil {
		return err
	}
	if existing != 0 {
		return ErrWalletRefundConflict
	}
	payments, err := s.Sources.LockRechargeSources(tx, req.UserID)
	if err != nil {
		return err
	}
	left := req.AmountCents
	now := time.Now().UTC()
	for _, pay := range payments {
		if !pay.TransactionConfirmed || pay.PaidCents != pay.TotalCents {
			continue
		}
		reserved, err := s.Recorder.ReservedInTx(tx, pay.ID)
		if err != nil {
			return err
		}
		amount := min(left, pay.PaidCents-pay.RefundedCents-reserved)
		if amount <= 0 {
			continue
		}
		sum := sha256.Sum256([]byte(req.RequestID + ":" + strconv.FormatUint(pay.ID, 10)))
		refundID, err := s.Recorder.CreateInTx(tx, money.RefundDraft{RefundNo: "WR" + hex.EncodeToString(sum[:16]), PaymentOrderID: pay.ID, UserID: req.UserID, BizType: "wallet_recharge", BizID: pay.BizID, AmountCents: amount, Reason: req.Reason, NextAttemptAt: now, CreatedMonth: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			return err
		}
		if err := tx.Table("wallet_refund_part").Create(map[string]any{"refund_record_id": refundID, "request_id": req.RequestID, "wallet_account_id": w.ID, "amount_cents": amount, "settled": false}).Error; err != nil {
			return err
		}
		left -= amount
		if left == 0 {
			break
		}
	}
	if left != 0 {
		return ErrWalletRefundConflict
	}
	if err := tx.Table("wallet_account").Where("id=?", w.ID).Updates(map[string]any{"frozen_cents": gorm.Expr("frozen_cents+?", req.AmountCents), "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	return nil
}
