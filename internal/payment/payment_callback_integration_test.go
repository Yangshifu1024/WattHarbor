//go:build integration

package payment

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"gorm.io/gorm"
)

// 支付回调的真库测试：并发重复投递、余额只入账一次。
//
// 微信支付会重投回调，而钱包充值直接改余额——这是"钱"上最典型的竞态。
// 单测能验的是 entryRejected 与 receiptMatches 这类纯判断，
// 验不了的是「两笔并发回调会不会把余额加两次」，那只有在真库的行锁、
// 事务与唯一约束下才存在。

const testMySQLURLEnv = "TEST_MYSQL_URL"

func testConn(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	raw := os.Getenv(testMySQLURLEnv)
	if raw == "" {
		t.Fatalf("%s 未设置。真库集成测试不提供跳过路径：没有库就是没测，不是测过了。", testMySQLURLEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sqlDB, err := dbconn.Open(ctx, raw)
	if err != nil {
		t.Fatalf("连接 %s 失败：%v", raw, err)
	}
	orm, err := dbconn.WrapGORM(sqlDB)
	if err != nil {
		t.Fatalf("包装 GORM 失败：%v", err)
	}
	return orm, func() { _ = sqlDB.Close() }
}

// lockGate 把一个事务卡在某条带 FOR UPDATE 的语句返回之后。
type lockGate struct {
	locked  chan struct{}
	release chan struct{}
	once    sync.Once
	passOne sync.Once
}

func newLockGate() *lockGate {
	return &lockGate{locked: make(chan struct{}), release: make(chan struct{})}
}

func (g *lockGate) await(t *testing.T, what string) {
	t.Helper()
	select {
	case <-g.locked:
	case <-time.After(15 * time.Second):
		t.Fatalf("%s 迟迟没有到达加锁语句——锁子句可能已改名或去掉", what)
	}
}

func (g *lockGate) pass() { g.passOne.Do(func() { close(g.release) }) }

// armOnPaymentOrderLock 装回调：本次执行里第一次锁定 payment_order 行后挂起。
func armOnLock(t *testing.T, db *gorm.DB, gate *lockGate, name, wantSQLFragment string) {
	t.Helper()
	err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if !strings.Contains(tx.Statement.SQL.String(), "FOR UPDATE") {
			return
		}
		if !strings.Contains(tx.Statement.SQL.String(), wantSQLFragment) {
			return
		}
		gate.once.Do(func() {
			close(gate.locked)
			<-gate.release
		})
	})
	if err != nil {
		t.Fatalf("注册加锁回调失败：%v", err)
	}
}

const (
	rechargeUserID = uint64(31001)
	rechargeCents  = int64(5000)
	rechargePaidAt = 0
)

// seedWalletRecharge 造一笔待结算的充值支付单。
func seedWalletRecharge(t *testing.T, db *gorm.DB, orderNo string) {
	t.Helper()
	// 先清干净再落数据：同一组 id 被两个测试复用，
	// 依赖前一个测试的收尾，会让它失败时把后面一起带崩。
	cleanupWalletRecharge(t, db, orderNo)
	if err := db.Table("user").Create(map[string]any{
		"id": rechargeUserID, "openid": "ox-recharge-1", "phone": "13900003101",
		"status": "active",
	}).Error; err != nil {
		t.Fatalf("造用户失败：%v", err)
	}
	if err := db.Table("wallet_account").Create(map[string]any{
		"id": 31001, "user_id": rechargeUserID, "balance_cents": 1000, "version": 1,
		"frozen_cents": 0, "status": "active",
	}).Error; err != nil {
		t.Fatalf("造钱包账户失败：%v", err)
	}
	var paymentOrderID uint64
	if err := db.Table("payment_order").Create(map[string]any{
		"order_no": orderNo, "biz_type": "wallet_recharge", "biz_id": 0,
		"user_id": rechargeUserID, "pay_method": "wechat", "total_cents": rechargeCents,
		"status": "initiated", "paid_cents": 0, "created_month": "2026-03-01",
		"expired_at": time.Now().Add(5 * time.Minute).UTC(),
	}).Error; err != nil {
		t.Fatalf("造支付单失败：%v", err)
	}
	if err := db.Raw("SELECT LAST_INSERT_ID()").Scan(&paymentOrderID).Error; err != nil {
		t.Fatalf("取支付单主键失败：%v", err)
	}
	if err := db.Table("wallet_recharge_request").Create(map[string]any{
		"request_id": "RC-" + orderNo, "user_id": rechargeUserID,
		"amount_cents": rechargeCents, "payment_order_id": paymentOrderID,
	}).Error; err != nil {
		t.Fatalf("造充值申请失败：%v", err)
	}
}

func cleanupWalletRecharge(t *testing.T, db *gorm.DB, orderNo string) {
	t.Helper()
	for _, q := range []struct {
		table, where string
		args         []any
	}{
		{"event_outbox", "envelope_json LIKE ?", []any{"%" + orderNo + "%"}},
		{"wallet_txn", "biz_ref LIKE ?", []any{"RC-" + orderNo + "%"}},
		{"wallet_recharge_request", "request_id LIKE ?", []any{"RC-" + orderNo}},
		{"payment_callback_idempotent", "wechat_transaction_id LIKE ?", []any{"42000-" + orderNo}},
		{"payment_order", "order_no = ?", []any{orderNo}},
		{"wallet_account", "user_id = ?", []any{rechargeUserID}},
		{"user", "id = ?", []any{rechargeUserID}},
	} {
		if err := db.Table(q.table).Where(q.where, q.args...).Delete(nil).Error; err != nil {
			t.Errorf("清理 %s 失败：%v", q.table, err)
		}
	}
}

func verifiedRecharge(orderNo string) VerifiedPayment {
	return VerifiedPayment{
		Provider: "wechat", MerchantID: "MCH-TEST", AppID: "wxapp-test",
		MerchantOrderNo: orderNo, TransactionID: "42000-" + orderNo,
		OpenID: "ox-recharge-1", PaidCents: rechargeCents,
		PaidAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
	}
}

// TestConcurrentWalletRechargeCallbacksCreditOnce 钉住并发重复回调只入账一次。
//
// 微信会重投回调；两笔并发投递必须只有一笔真正加余额，
// 另一笔识别为重放。断言三件事：余额只加一次、钱包流水只有一条、
// 两笔调用中恰好一笔标记为重放且都不报错。
func TestConcurrentWalletRechargeCallbacksCreditOnce(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	const orderNo = "P-CONC-RECHARGE-1"
	// 清理必须先于播种注册：中途失败时种子数据可能只落了一半，
	// 放到播种之后再注册的话，这半截就留���库里了。
	// 两步都走 t.Cleanup：注册在前的后执行（LIFO），
	// 于是先删数据再关连接。用 defer 关连接会抢在 t.Cleanup 之前，
	// 清理时连接已关，只会刷出一串 "database is closed"。
	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupWalletRecharge(t, janitor, orderNo) })
	seedWalletRecharge(t, setup, orderNo)

	p1, close1 := testConn(t)
	defer close1()
	p2, close2 := testConn(t)
	defer close2()

	gate := newLockGate()
	armOnLock(t, p1, gate, "test:payment_lock", "payment_order")
	defer gate.pass()

	store1 := PaymentCallbackStore{DB: p1, ExpectedProvider: "wechat", ExpectedMerchantID: "MCH-TEST", ExpectedAppID: "wxapp-test"}
	store2 := PaymentCallbackStore{DB: p2, ExpectedProvider: "wechat", ExpectedMerchantID: "MCH-TEST", ExpectedAppID: "wxapp-test"}

	type outcome struct {
		res PaymentCallbackResult
		err error
	}
	done1 := make(chan outcome, 1)
	go func() {
		res, err := store1.Apply(context.Background(), verifiedRecharge(orderNo))
		done1 <- outcome{res, err}
	}()

	// 等第一笔确实锁住了支付单行。
	gate.await(t, "第一笔回调")

	done2 := make(chan outcome, 1)
	go func() {
		res, err := store2.Apply(context.Background(), verifiedRecharge(orderNo))
		done2 <- outcome{res, err}
	}()

	// 第二笔此刻必须被行锁挡住，而不是自己再加一次余额。
	select {
	case got := <-done2:
		t.Fatalf("第一笔仍持锁时第二笔就返回了（%+v，%v）——回调没有串行化", got.res, got.err)
	case <-time.After(2 * time.Second):
	}

	gate.pass()
	first := <-done1
	if first.err != nil {
		t.Fatalf("第一笔回调失败：%v", first.err)
	}
	second := <-done2
	if second.err != nil {
		t.Fatalf("第二笔回调不应报错（应识别为重放）：%v", second.err)
	}

	replayed := 0
	if first.res.Replayed {
		replayed++
	}
	if second.res.Replayed {
		replayed++
	}
	if replayed != 1 {
		t.Errorf("两笔回调中恰好一笔应标记重放，实际 %d 笔（第一笔 %+v，第二笔 %+v）", replayed, first.res, second.res)
	}

	// 余额只应增加一笔，且只留下一条钱包流水。
	verify, closeVerify := testConn(t)
	defer closeVerify()
	var balance, version int64
	if err := verify.Table("wallet_account").
		Where("user_id = ?", rechargeUserID).
		Select("balance_cents, version").Row().Scan(&balance, &version); err != nil {
		t.Fatalf("读钱包余额失败：%v", err)
	}
	if want := int64(1000 + rechargeCents); balance != want {
		t.Errorf("余额 = %d，期望 %d（起始 1000 + 充值 %d）——并发重复回调多加了钱", balance, want, rechargeCents)
	}
	var txns int64
	if err := verify.Table("wallet_txn").Where("user_id = ?", rechargeUserID).Count(&txns).Error; err != nil {
		t.Fatalf("统计钱包流水失败：%v", err)
	}
	if txns != 1 {
		t.Errorf("钱包流水 %d 条，期望 1 条", txns)
	}
}

// TestConcurrentRechargesForSameUserDoNotLoseUpdate 钉住同一用户两笔充值并发到账。
//
// 上一组测试里两笔回调锁的是**同一张支付单**，最外层的行锁已经把它们完全串行化，
// 下游的钱包行锁因此永远不会被单独触发——删掉它，那组测试照样通过。
// 本组换场景：同一个用户的两笔**不同**充值单同时到账。外层锁的是两行不同的记录，
// 互不阻塞，两笔都会同时走到钱包余额更新。
//
// 丢了 wallet_account 的行锁，第二笔会读到自己的快照旧值，两笔都按同一个基数
// 算出余额，后写的覆盖先写的——到账 10000，余额却只增加 5000。
func TestConcurrentRechargesForSameUserDoNotLoseUpdate(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	const orderA, orderB = "P-SAMEUSER-A", "P-SAMEUSER-B"

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() {
		cleanupWalletRecharge(t, janitor, orderA)
		cleanupWalletRecharge(t, janitor, orderB)
	})
	seedWalletRecharge(t, setup, orderA)
	seedWalletRecharge(t, setup, orderB)

	p1, close1 := testConn(t)
	defer close1()
	p2, close2 := testConn(t)
	defer close2()

	// 闸门卡在**钱包账户**的加锁读上，而不是支付单上。
	gate := newLockGate()
	armOnLock(t, p1, gate, "test:wallet_lock", "wallet_account")
	defer gate.pass()

	store1 := PaymentCallbackStore{DB: p1, ExpectedProvider: "wechat", ExpectedMerchantID: "MCH-TEST", ExpectedAppID: "wxapp-test"}
	store2 := PaymentCallbackStore{DB: p2, ExpectedProvider: "wechat", ExpectedMerchantID: "MCH-TEST", ExpectedAppID: "wxapp-test"}

	type outcome struct {
		res PaymentCallbackResult
		err error
	}
	done1 := make(chan outcome, 1)
	done2 := make(chan outcome, 1)
	go func() {
		res, err := store1.Apply(context.Background(), verifiedRecharge(orderA))
		done1 <- outcome{res, err}
	}()
	gate.await(t, "第一笔到账（已锁住钱包账户）")

	go func() {
		res, err := store2.Apply(context.Background(), verifiedRecharge(orderB))
		done2 <- outcome{res, err}
	}()

	// 第一笔持锁未提交时，第二笔必须被挡在钱包账户行锁外面。
	select {
	case got := <-done2:
		t.Fatalf("第一笔仍持钱包锁时第二笔就返回了（%+v，%v）——wallet_account 未加行锁，并发到账会丢更新", got.res, got.err)
	case <-time.After(2 * time.Second):
	}

	gate.pass()
	if first := <-done1; first.err != nil {
		t.Fatalf("第一笔到账失败：%v", first.err)
	}
	if second := <-done2; second.err != nil {
		t.Fatalf("第二笔到账失败：%v", second.err)
	}

	verify, closeVerify := testConn(t)
	defer closeVerify()
	var balance int64
	if err := verify.Table("wallet_account").Where("user_id = ?", rechargeUserID).
		Select("balance_cents").Row().Scan(&balance); err != nil {
		t.Fatalf("读钱包余额失败：%v", err)
	}
	if want := int64(1000 + 2*rechargeCents); balance != want {
		t.Errorf("余额 = %d，期望 %d（起始 1000 + 两笔各 %d）——并发到账丢了更新", balance, want, rechargeCents)
	}
	var txns int64
	if err := verify.Table("wallet_txn").Where("user_id = ?", rechargeUserID).Count(&txns).Error; err != nil {
		t.Fatalf("统计钱包流水失败：%v", err)
	}
	if txns != 2 {
		t.Errorf("钱包流水 %d 条，期望 2 条", txns)
	}
}
