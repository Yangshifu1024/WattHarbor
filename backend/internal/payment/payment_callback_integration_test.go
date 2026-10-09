//go:build integration

package payment

import (
	"chargepilot/internal/testfixture"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"chargepilot/internal/platform/dbconn"
	"gorm.io/gorm"
)

// 使用真实 MySQL 行锁、事务与唯一键验证并发回调只入账一次。
// 渠道验签由构造的已验证通知替代，不代表真实支付验收。

const testMySQLURLEnv = "TEST_MYSQL_URL"

// testConn 接收测试句柄 t，从必需的 TEST_MYSQL_URL 以十秒初始化 context 打开真实 SQL 池并包装 GORM。
// 成功返回 ORM 与关闭函数，由调用方在事务及任务结束后调用；缺配置、连接或包装错误由 t.Fatal 终止，不跳过测试。
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
	// locked 首次取得目标行锁后关闭的通知通道，由数据库回调关闭，控制方只接收。
	locked chan struct{}
	// release 控制方关闭后允许加锁位置回调继续的闸门，不提交数据库事务或自行释放行锁。
	release chan struct{}
	once    sync.Once
	passOne sync.Once
}

// newLockGate 创建锁等待通知与放行通道，不启动任务。
func newLockGate() *lockGate {
	return &lockGate{locked: make(chan struct{}), release: make(chan struct{})}
}

// await 最多等待十五秒确认事务已取得目标锁，超时终止测试。
func (g *lockGate) await(t *testing.T, what string) {
	t.Helper()
	select {
	case <-g.locked:
	case <-time.After(15 * time.Second):
		t.Fatalf("%s 迟迟没有到达加锁语句——锁子句可能已改名或去掉", what)
	}
}

// pass 放行测试事务；可重复调用，不提交或释放数据库锁。
func (g *lockGate) pass() { g.passOne.Do(func() { close(g.release) }) }

// armOnPaymentOrderLock 装回调：本次执行里第一次锁定 payment_order 行后挂起。
func armOnLock(t *testing.T, db *gorm.DB, gate *lockGate, name, wantTable string) {
	t.Helper()
	err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if !strings.Contains(tx.Statement.SQL.String(), "FOR UPDATE") {
			return
		}
		if !strings.Contains(tx.Statement.SQL.String(), "FROM `"+wantTable+"`") {
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
	// 先清干净再落数据：同一组 id 被两个测试复用。
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

// cleanupWalletRecharge 清理本组固定充值身份的回执、流水、申请、支付、钱包、用户及 Outbox。
// 逐项记录清理错误；调用方须独占这些身份，清理后才能关闭连接。
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
		var err error
		if q.table == "event_outbox" {
			err = testfixture.DeleteOutbox(db, q.where, q.args...)
		} else {
			err = db.Table(q.table).Where(q.where, q.args...).Delete(nil).Error
		}
		if err != nil {
			t.Errorf("清理 %s 失败：%v", q.table, err)
		}
	}
}

// verifiedRecharge 构造固定测试标识、五千分金额与 UTC 支付时刻的通知。
// 交易号由支付单号派生，只替代渠道验签，不实际验签。
func verifiedRecharge(orderNo string) VerifiedPayment {
	return VerifiedPayment{
		Provider: "wechat", MerchantID: "MCH-TEST", AppID: "wxapp-test",
		MerchantOrderNo: orderNo, TransactionID: "42000-" + orderNo,
		OpenID: "ox-recharge-1", PaidCents: rechargeCents,
		PaidAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
	}
}

// TestConcurrentWalletRechargeCallbacksCreditOnce 验证相同回调并发时只加一次余额、留一条流水，另一调用识别重放。
func TestConcurrentWalletRechargeCallbacksCreditOnce(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	const orderNo = "P-CONC-RECHARGE-1"
	// 造数前注册清理，确保部分写入后失败也能删除测试记录。
	// t.Cleanup 按注册的反向顺序执行，须先删数据再关闭连接。
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

	store1 := CallbackService{DB: p1, ExpectedProvider: "wechat", ExpectedMerchantID: "MCH-TEST", ExpectedAppID: "wxapp-test"}
	store2 := CallbackService{DB: p2, ExpectedProvider: "wechat", ExpectedMerchantID: "MCH-TEST", ExpectedAppID: "wxapp-test"}

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

// TestConcurrentRechargesForSameUserDoNotLoseUpdate 验证同用户两笔不同充值并发到账不会丢失更新。
// 支付行互不阻塞，测试在钱包锁后挂起第一事务，确认第二事务必须等锁。
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

	store1 := CallbackService{DB: p1, ExpectedProvider: "wechat", ExpectedMerchantID: "MCH-TEST", ExpectedAppID: "wxapp-test"}
	store2 := CallbackService{DB: p2, ExpectedProvider: "wechat", ExpectedMerchantID: "MCH-TEST", ExpectedAppID: "wxapp-test"}

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
