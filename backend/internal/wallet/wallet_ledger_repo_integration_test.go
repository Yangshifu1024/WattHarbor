//go:build integration

package wallet

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"wattharbor/internal/platform/dbutil"
	"wattharbor/internal/testfixture"
)

func TestMoveWritesMonthAnchorAndBalance(t *testing.T) {
	db := walletDB(t)
	for _, tc := range []struct {
		name      string
		cents     int64
		direction string
		biz       string
	}{
		{name: "credit", cents: 75, direction: "in", biz: "refund"},
		{name: "debit", cents: -75, direction: "out", biz: "pay"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := walletTransaction(t, db)
			id := insertLedgerWallet(t, tx, 100, 20, "active")
			w, err := Lock(tx, id)
			if err != nil {
				t.Fatal(err)
			}
			ref := "ledger-" + uuid.NewString()
			before := dbutil.MonthStart()
			if err := Move(tx, id, &w, tc.cents, ref, "month anchor fixture"); err != nil {
				t.Fatal(err)
			}
			after := dbutil.MonthStart()
			assertLedgerWallet(t, tx, id, 100+tc.cents, 20, 2)
			var ledger struct {
				Direction         string
				BizType           string
				AmountCents       int64
				BalanceAfterCents int64
				CreatedMonth      time.Time
			}
			if err := tx.Table("wallet_txn").Where("user_id = ? AND txn_no = ?", id, ref).Take(&ledger).Error; err != nil {
				t.Fatal(err)
			}
			amount := tc.cents
			if amount < 0 {
				amount = -amount
			}
			if ledger.Direction != tc.direction || ledger.BizType != tc.biz || ledger.AmountCents != amount || ledger.BalanceAfterCents != 100+tc.cents {
				t.Fatalf("wrong ledger: %+v", ledger)
			}
			if !ledger.CreatedMonth.Equal(before) && !ledger.CreatedMonth.Equal(after) {
				t.Fatalf("created_month=%v want %v or %v", ledger.CreatedMonth, before, after)
			}
			if ledger.CreatedMonth.Day() != 1 {
				t.Fatalf("created_month=%v is not month start", ledger.CreatedMonth)
			}
		})
	}
}

func TestMoveRejectsInvalidBalancesWithoutWrites(t *testing.T) {
	db := walletDB(t)
	for _, tc := range []struct {
		name    string
		balance int64
		frozen  int64
		status  string
		cents   int64
		mutate  func(*Row) *Row
		want    error
	}{
		{name: "overflow", balance: math.MaxInt64, cents: 1, want: ErrInvalidBalance},
		{name: "minimum_delta", balance: 100, cents: math.MinInt64, want: ErrInvalidBalance},
		{name: "nil_projection", balance: 100, cents: 1, mutate: func(*Row) *Row { return nil }, want: ErrInvalidBalance},
		{name: "negative_projection_balance", balance: 100, cents: 1, mutate: func(w *Row) *Row { w.BalanceCents = -1; return w }, want: ErrInvalidBalance},
		{name: "negative_projection_frozen", balance: 100, cents: 1, mutate: func(w *Row) *Row { w.FrozenCents = -1; return w }, want: ErrInvalidBalance},
		{name: "projection_frozen_exceeds_balance", balance: 100, cents: 1, mutate: func(w *Row) *Row { w.FrozenCents = 101; return w }, want: ErrInvalidBalance},
		{name: "reserved_balance_not_spendable", balance: 100, frozen: 80, cents: -21, want: ErrInsufficient},
		{name: "inactive_debit", balance: 100, status: "frozen", cents: -1, want: ErrInactive},
		{name: "zero_movement", balance: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := walletTransaction(t, db)
			status := tc.status
			if status == "" {
				status = "active"
			}
			id := insertLedgerWallet(t, tx, tc.balance, tc.frozen, status)
			w, err := Lock(tx, id)
			if err != nil {
				t.Fatal(err)
			}
			projection := &w
			if tc.mutate != nil {
				projection = tc.mutate(&w)
			}
			if err := Move(tx, id, projection, tc.cents, "ledger-"+uuid.NewString(), "rejection fixture"); !errors.Is(err, tc.want) {
				t.Fatalf("Move err=%v want=%v", err, tc.want)
			}
			assertLedgerWallet(t, tx, id, tc.balance, tc.frozen, 1)
			var count int64
			if err := tx.Table("wallet_txn").Where("user_id = ?", id).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("rejected movement left ledger count=%d err=%v", count, err)
			}
		})
	}
}

// TestMoveDuplicateLedgerRollsBackBalance 验证流水唯一键失败时调用方回滚同次余额及版本更新。
func TestMoveDuplicateLedgerRollsBackBalance(t *testing.T) {
	tx := walletTransaction(t, walletDB(t))
	id := insertLedgerWallet(t, tx, 100, 20, "active")
	ref := "ledger-" + uuid.NewString()
	w, err := Lock(tx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := Move(tx, id, &w, 25, ref, "original"); err != nil {
		t.Fatal(err)
	}
	err = tx.Transaction(func(attempt *gorm.DB) error {
		current, err := Lock(attempt, id)
		if err != nil {
			return err
		}
		return Move(attempt, id, &current, 50, ref, "duplicate")
	})
	if !dbutil.IsMySQLDuplicate(err) {
		t.Fatalf("duplicate movement err=%v", err)
	}
	assertLedgerWallet(t, tx, id, 125, 20, 2)
	var count int64
	if err := tx.Table("wallet_txn").Where("user_id = ?", id).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("duplicate ledger count=%d err=%v", count, err)
	}
}

func insertLedgerWallet(t *testing.T, tx *gorm.DB, balance, frozen int64, status string) uint64 {
	t.Helper()
	id := testfixture.NewID()
	if err := tx.Table("wallet_account").Create(map[string]any{"id": id, "user_id": id, "balance_cents": balance, "frozen_cents": frozen, "status": status, "version": 1}).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func assertLedgerWallet(t *testing.T, tx *gorm.DB, id uint64, balance, frozen int64, version uint64) {
	t.Helper()
	var stored struct {
		BalanceCents int64
		FrozenCents  int64
		Version      uint64
	}
	if err := tx.Table("wallet_account").Where("id = ?", id).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.BalanceCents != balance || stored.FrozenCents != frozen || stored.Version != version {
		t.Fatalf("wallet=%+v want balance=%d frozen=%d version=%d", stored, balance, frozen, version)
	}
}
