//go:build integration

package settlement

import (
	"context"
	"crypto/rand"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/ops"
	"wattharbor/internal/payment"
	"wattharbor/internal/testfixture"
	"wattharbor/internal/wallet"
)

// TestRiskApprovalSeesRechargeCommittedDuringWalletLockWait 验证审核读取申请后等待钱包锁时仍采用新到账来源。
// 两条独立连接与指定钱包锁闸门；到账、来源、预占和审计实际提交，结束后按唯一身份清理为零。
func TestRiskApprovalSeesRechargeCommittedDuringWalletLockWait(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	first, closeFirst := testConn(t)
	defer closeFirst()
	second, closeSecond := testConn(t)
	defer closeSecond()
	id := testfixture.NewID()
	request := uuid.NewString()
	recharge := uuid.NewString()
	openid := "risk-wait-" + rand.Text()
	no := "RW" + rand.Text()
	defer func() {
		for _, q := range []struct {
			table string // 本测试实际提交的表
			where string // 唯一业务 ID，避免删除已有开发数据
			arg   any    // 筛选用户或首次请求号
		}{{"wallet_refund_part", "request_id=?", request}, {"wallet_risk_review", "request_id=?", request}, {"wallet_risk_freeze_link", "request_id=?", request}, {"wallet_refund_request", "request_id=?", request}, {"risk_freeze_log", "user_id=?", id}, {"refund_record", "user_id=?", id}, {"event_outbox", "envelope_json LIKE ?", "%" + recharge + "%"}, {"audit_log", "request_id=?", request}, {"wallet_txn", "user_id=?", id}, {"wallet_recharge_request", "request_id=?", recharge}, {"payment_callback_idempotent", "wechat_transaction_id=?", "TX-" + no}, {"payment_order", "id=?", id}, {"wallet_account", "id=?", id}, {"user", "id=?", id}} {
			var err error
			if q.table == "event_outbox" {
				err = testfixture.DeleteOutbox(setup, q.where, q.arg)
			} else {
				err = setup.Table(q.table).Where(q.where, q.arg).Delete(nil).Error
			}
			if err != nil {
				t.Error(err)
			}
			var n int64
			if err := setup.Table(q.table).Where(q.where, q.arg).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("%s remaining=%d %v", q.table, n, err)
			}
		}
	}()
	for _, row := range []struct {
		table  string         // 用户、钱包、支付及申请所属表
		values map[string]any // 测试所需字段
	}{{"user", map[string]any{"id": id, "openid": openid, "status": "active"}}, {"wallet_account", map[string]any{"id": id, "user_id": id, "balance_cents": 0, "frozen_cents": 0, "status": "frozen", "version": 1}}, {"payment_order", map[string]any{"id": id, "order_no": no, "biz_type": "wallet_recharge", "biz_id": 0, "user_id": id, "pay_method": "wechat", "total_cents": 700, "paid_cents": 0, "status": "initiated", "created_month": time.Now().UTC().Format("2006-01") + "-01"}}, {"wallet_recharge_request", map[string]any{"request_id": recharge, "user_id": id, "amount_cents": 700, "payment_order_id": id}}} {
		if err := setup.Table(row.table).Create(row.values).Error; err != nil {
			t.Fatal(err)
		}
	}
	repo := wallet.UserWalletRepo{DB: setup}
	if err := repo.InsertRefundRequest(setup, request, id, 500, nil); err != nil {
		t.Fatal(err)
	}
	freeze, err := repo.InsertRiskFreeze(setup, id, "wallet_refund_frequency", "wallet", "approval wait")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkFreezeToRefund(setup, request, freeze); err != nil {
		t.Fatal(err)
	}
	gate := newLockGate()
	defer gate.pass()
	key := "risk_wait_" + rand.Text()
	armOnLock(t, first, gate, key, "wallet_account")
	defer first.Callback().Query().Remove(key)
	attempting := make(chan struct{})
	var once sync.Once
	if err := second.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table != "wallet_account" {
			return
		}
		if _, ok := q.Statement.Clauses["FOR"]; !ok {
			return
		}
		once.Do(func() { close(attempting) })
	}); err != nil {
		t.Fatal(err)
	}
	defer second.Callback().Query().Remove(key)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	callbackDone := make(chan error, 1)
	go func() {
		_, err := (payment.CallbackService{DB: first, ExpectedProvider: "wechat_direct", ExpectedMerchantID: "merchant", ExpectedAppID: "app"}).Apply(ctx, payment.VerifiedPayment{Provider: "wechat_direct", MerchantID: "merchant", AppID: "app", MerchantOrderNo: no, TransactionID: "TX-" + no, OpenID: openid, PaidCents: 700, PaidAt: time.Now().UTC()})
		callbackDone <- err
	}()
	gate.await(t, "callback wallet lock")
	approved := true
	audit := ops.AuditContext{Actor: operations.Profile{ID: id, Username: "risk-wait", Role: "customer_finance", Permissions: []string{"finance.wallet_risk.review"}}, RequestID: request}
	riskDone := make(chan error, 1)
	go func() {
		_, err := (wallet.RiskService{DB: second, Refund: wallet.RefundService{Sources: payment.RechargeSources{}, Recorder: RefundRecorder{}}}).Review(ctx, audit, request, wallet.RiskReviewInput{Approved: &approved, Comment: "最新到账可退"})
		riskDone <- err
	}()
	select {
	case <-attempting:
	case <-ctx.Done():
		t.Fatal("risk did not reach wallet lock")
	}
	gate.pass()
	for _, ch := range []<-chan error{callbackDone, riskDone} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	w, err := repo.LockWalletAccount(setup, id)
	if err != nil || w.BalanceCents != 700 || w.FrozenCents != 500 || w.Status != "frozen" {
		t.Fatalf("new credit not reserved: %+v %v", w, err)
	}
	var n int64
	if err := setup.Table("audit_log").Where("target_type='wallet_risk' AND target_id=?", strconv.FormatUint(id, 10)).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("audit=%d %v", n, err)
	}
}

// TestWalletRiskServiceKeepsReservationReviewReleaseAndAuditAtomic 验证风控用例拥有全部资金与审计事务。
// 钱包、支付来源、退款及风险记录均为实际 MySQL；无外部退款请求，全部造数与结果外层回滚。
func TestWalletRiskServiceKeepsReservationReviewReleaseAndAuditAtomic(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	ctx := context.Background()
	id := testfixture.NewID()
	request := uuid.NewString()
	if err := tx.Table("user").Create(map[string]any{"id": id, "openid": "risk-" + rand.Text(), "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("wallet_account").Create(map[string]any{"id": id, "user_id": id, "balance_cents": 1000, "frozen_cents": 0, "status": "frozen", "version": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("payment_order").Create(map[string]any{"id": id, "order_no": "P-RISK-" + rand.Text(), "biz_type": "wallet_recharge", "biz_id": 0, "user_id": id, "pay_method": "wechat", "total_cents": 1000, "paid_cents": 1000, "status": "paid", "wechat_transaction_id": "TX-" + rand.Text(), "created_month": "2026-10-01"}).Error; err != nil {
		t.Fatal(err)
	}
	repo := wallet.UserWalletRepo{DB: tx}
	if err := repo.InsertRefundRequest(tx, request, id, 500, nil); err != nil {
		t.Fatal(err)
	}
	freeze, err := repo.InsertRiskFreeze(tx, id, "wallet_refund_frequency", "wallet", "risk test")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkFreezeToRefund(tx, request, freeze); err != nil {
		t.Fatal(err)
	}
	s := wallet.RiskService{DB: tx, Refund: wallet.RefundService{Sources: payment.RechargeSources{}, Recorder: RefundRecorder{}}}
	audit := ops.AuditContext{Actor: operations.Profile{ID: 51, Username: "risk-review", Role: "customer_finance", Permissions: []string{"finance.wallet_risk.review", "finance.wallet_risk.release"}}, RequestID: uuid.NewString()}
	approved := true
	in := wallet.RiskReviewInput{Approved: &approved, Comment: "审核通过"}
	fault := true
	key := "risk_audit_" + rand.Text()
	if err := db.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
		if fault && q.Statement.Table == "audit_log" {
			q.AddError(errors.New("risk audit unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Create().Remove(key)
	if _, err := s.Review(ctx, audit, request, in); err == nil {
		t.Fatal("audit failure committed reservation")
	}
	for _, table := range []string{"wallet_refund_part", "wallet_risk_review"} {
		var n int64
		if err := tx.Table(table).Where("request_id=?", request).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("%s survived rollback: %d %v", table, n, err)
		}
	}
	var n int64
	if err := tx.Table("refund_record").Where("user_id=?", id).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("refund survived rollback: %d %v", n, err)
	}
	w, err := repo.LockWalletAccount(tx, id)
	if err != nil || w.FrozenCents != 0 || w.Version != 1 {
		t.Fatalf("funds survived rollback: %+v %v", w, err)
	}
	fault = false
	first, err := s.Review(ctx, audit, request, in)
	if err != nil || first["approved"] != true {
		t.Fatalf("review %+v %v", first, err)
	}
	if _, err := s.Review(ctx, audit, request, in); err != nil {
		t.Fatalf("review replay %v", err)
	}
	changed := in
	changed.Comment = "不同依据"
	if _, err := s.Review(ctx, audit, request, changed); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("changed review accepted: %v", err)
	}
	w, err = repo.LockWalletAccount(tx, id)
	if err != nil || w.BalanceCents != 1000 || w.FrozenCents != 500 || w.Status != "frozen" || w.Version != 2 {
		t.Fatalf("approval not pending reservation: %+v %v", w, err)
	}
	if err := tx.Table("refund_record").Where("user_id=? AND status='pending'", id).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("pending refunds=%d %v", n, err)
	}
	cs := audit
	cs.Actor.Role = "customer_cs"
	if _, err := s.Release(ctx, cs, request, wallet.RiskReleaseInput{Comment: "解冻"}); !errors.Is(err, operations.ErrForbidden) {
		t.Fatalf("customer service release accepted: %v", err)
	}
	fault = true
	if _, err := s.Release(ctx, audit, request, wallet.RiskReleaseInput{Comment: "解冻"}); err == nil {
		t.Fatal("audit failure committed release")
	}
	w, err = repo.LockWalletAccount(tx, id)
	if err != nil || w.Status != "frozen" || w.Version != 2 {
		t.Fatalf("release survived rollback: %+v %v", w, err)
	}
	fault = false
	for i := 0; i < 2; i++ {
		out, err := s.Release(ctx, audit, request, wallet.RiskReleaseInput{Comment: "解冻"})
		if err != nil || out["wallet_active"] != true {
			t.Fatalf("release replay %+v %v", out, err)
		}
	}
	w, err = repo.LockWalletAccount(tx, id)
	if err != nil || w.Status != "active" || w.Version != 3 || w.FrozenCents != 500 || w.BalanceCents != 1000 {
		t.Fatalf("release settled funds or repeated version: %+v %v", w, err)
	}
	if err := tx.Table("audit_log").Where("module='wallet_risk' AND request_id=?", request).Count(&n).Error; err != nil || n != 2 {
		t.Fatalf("risk audit count=%d %v", n, err)
	}
}
