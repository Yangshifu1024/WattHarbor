//go:build integration

package settlement

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/testfixture"
	"testing"
	"time"

	pay "github.com/ChargePilot2026/charge-pilot/internal/payment"
	"gorm.io/gorm"
)

// 退款成功入账的真库并发测试（RefundExecutor.apply 的 SUCCESS 分支）。
//
// apply 拿的是「钱包分摊行 → payment_order 行锁 → refund_record 行锁」，
// 钱账的累加读 `p.RefundedCents` 走的是那把行锁的当前读，所以累加本身应当是安全的。
// 单测只覆盖 receiptMatches 与 providerMatchesChannel 这两个纯函数，
// 验不了「同一笔退款被并发入账两次」与「同一支付单上两笔退款并发累加」。

const (
	execUserID   = uint64(43001)
	execPayOrdID = uint64(43001)
	execChargeID = uint64(43001)
	execPaid     = int64(10000)
)

// execRefund 造一条处于 processing 的自动退款单。
//
// biz_type 用 charge：那样 LockRefundWallet 与 SettleRefundWallet 会直接返回，
// 测试要落的表就只有 payment_order、refund_record、charge_order 三张。
func execRefund(t *testing.T, db *gorm.DB, id uint64, refundNo string, cents int64) {
	t.Helper()
	if err := db.Table("refund_record").Create(map[string]any{
		"id": id, "refund_no": refundNo, "payment_order_id": execPayOrdID,
		"user_id": execUserID, "biz_type": "charge", "biz_id": execChargeID,
		"refund_cents": cents, "status": "processing", "execution_policy": "automatic",
		"retry_count": 1, "next_attempt_at": time.Now().Add(-time.Minute).UTC(),
		"created_month": "2026-03-01",
	}).Error; err != nil {
		t.Fatalf("造退款单 %d 失败：%v", id, err)
	}
}

// seedExecutableRefund 接收测试句柄 t 与真库句柄 db，插入固定一万分实付支付单及对应 refunding 充电订单，供退款成功入账验证。
// 无返回值，写入失败由 t.Fatal 终止；不创建退款单或新事务，调用方负责固定主键独占及已提交数据清理。
func seedExecutableRefund(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Table("payment_order").Create(map[string]any{
		"id": execPayOrdID, "order_no": "P-EXEC-1", "biz_type": "charge", "biz_id": execChargeID,
		"user_id": execUserID, "pay_method": "wechat", "total_cents": execPaid,
		"paid_cents": execPaid, "refunded_cents": 0, "status": "paid",
		"wechat_transaction_id": "SIM4200000002", "created_month": "2026-03-01",
	}).Error; err != nil {
		t.Fatalf("造支付单失败：%v", err)
	}
	if err := db.Table("charge_order").Create(map[string]any{
		"id": execChargeID, "order_no": "C-EXEC-1", "user_id": execUserID,
		"device_id": "D1", "port_no": 1, "status": "refunding",
		"payment_status": "paid", "payment_order_id": execPayOrdID,
		"total_cents": execPaid, "electric_cents": execPaid, "service_cents": 0,
		"created_month": "2026-03-01",
		"started_at":    time.Now().Add(-2 * time.Hour).UTC(),
		"ended_at":      time.Now().Add(-time.Hour).UTC(),
	}).Error; err != nil {
		t.Fatalf("造充电订单失败：%v", err)
	}
}

// cleanupExecutableRefund 接收测试句柄 t、真库句柄 db 与退款主键切片 refundIDs，删除对应成功回执、退款记录及固定充电／支付单。
// 发件箱只删除原退款来源与支付身份匹配的事件；无返回值，逐项错误由 t.Errorf 记录并继续，不重置数据库或关闭池。
func cleanupExecutableRefund(t *testing.T, db *gorm.DB, refundIDs []uint64) {
	t.Helper()
	// 仅清理由本测试退款身份生成的事件，避免删除其他包的开发或并发测试数据。
	if err := testfixture.DeleteOutbox(db, "stream=? AND JSON_EXTRACT(envelope_json,'$.payment_order_id')=?", "refund_succeeded_stream", execPayOrdID); err != nil {
		t.Errorf("清理本测试发件箱失败：%v", err)
	}
	for _, id := range refundIDs {
		if err := db.Table("refund_success_receipt").Where("refund_record_id = ?", id).Delete(nil).Error; err != nil {
			t.Errorf("清理退款回执 %d 失败：%v", id, err)
		}
		if err := db.Table("refund_record").Where("id = ?", id).Delete(nil).Error; err != nil {
			t.Errorf("清理退款单 %d 失败：%v", id, err)
		}
	}
	if err := db.Table("charge_order").Where("id = ?", execChargeID).Delete(nil).Error; err != nil {
		t.Errorf("清理充电订单失败：%v", err)
	}
	if err := db.Table("payment_order").Where("id = ?", execPayOrdID).Delete(nil).Error; err != nil {
		t.Errorf("清理支付单失败：%v", err)
	}
}

// execRecord 接收退款主键 id、退款单号 refundNo 与分为单位的 cents，返回关联固定支付、用户与充电身份的 processing RefundRecord。
// 不校验金额或写入数据库，无错误或外部副作用。
func execRecord(id uint64, refundNo string, cents int64) RefundRecord {
	return RefundRecord{
		ID: id, RefundNo: refundNo, PaymentOrderID: execPayOrdID, UserID: execUserID,
		BizType: "charge", BizID: execChargeID, RefundCents: cents, Status: "processing",
	}
}

// execRequest 接收退款单号 refundNo 与分为单位的 cents，返回固定商户单号、渠道流水号及一万分总额的 RefundRequest；不校验额度、调用渠道或创建任务。
func execRequest(refundNo string, cents int64) pay.RefundRequest {
	return pay.RefundRequest{
		RefundNo: refundNo, MerchantOrderNo: "P-EXEC-1",
		TransactionID: "SIM4200000002", TotalCents: execPaid, RefundCents: cents,
	}
}

// execResult 接收退款单号 refundNo、分为单位的 cents 与渠道退款身份 refundID，返回固定 SUCCESS 状态及 UTC 成功时刻的 RefundResult。
// 不调用渠道或确认到账，只为真实入账事务提供测试事实，无错误或 IO。
func execResult(refundNo string, cents int64, refundID string) pay.RefundResult {
	return pay.RefundResult{
		RefundNo: refundNo, MerchantOrderNo: "P-EXEC-1", TransactionID: "SIM4200000002",
		TotalCents: execPaid, RefundCents: cents, RefundID: refundID, Status: "SUCCESS",
		SuccessAt: time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC),
	}
}

// TestConcurrentApplySameRefundCreditsOnce 钉住同一笔退款并发入账只发生一次。
//
// 渠道结果未知时 executor 会反复重试，两次 apply 可能重叠。`refunded_cents`
// 只能加一次，`refund_success_receipt` 只能有一条，钱不能加两遍。
func TestConcurrentApplySameRefundCreditsOnce(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	const refundID = uint64(43001)
	const refundNo = "R-EXEC-SAME"

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupExecutableRefund(t, janitor, []uint64{refundID}) })
	cleanupExecutableRefund(t, setup, []uint64{refundID})
	seedExecutableRefund(t, setup)
	execRefund(t, setup, refundID, refundNo, 4000)

	p1, close1 := testConn(t)
	defer close1()
	p2, close2 := testConn(t)
	defer close2()

	gate := newLockGate()
	armOnLock(t, p1, gate, "test:apply_lock", "payment_order")
	defer gate.pass()

	e1 := RefundExecutor{DB: p1}
	e2 := RefundExecutor{DB: p2}
	done1 := make(chan error, 1)
	go func() {
		done1 <- e1.apply(context.Background(), execRecord(refundID, refundNo, 4000),
			execRequest(refundNo, 4000), execResult(refundNo, 4000, "WR-1"))
	}()
	gate.await(t, "第一笔入账")

	trying := armOnLockAttempt(t, p2, "test:apply_attempt", "payment_order")
	done2 := make(chan error, 1)
	go func() {
		done2 <- e2.apply(context.Background(), execRecord(refundID, refundNo, 4000),
			execRequest(refundNo, 4000), execResult(refundNo, 4000, "WR-1"))
	}()
	awaitLockAttempt(t, trying, "第二笔退款入账")

	gate.pass()
	if err := <-done1; err != nil {
		t.Fatalf("第一笔入账失败：%v", err)
	}
	if err := <-done2; err != nil {
		t.Fatalf("第二笔应识别为重放并直接返回，不该报错：%v", err)
	}

	verify, closeVerify := testConn(t)
	defer closeVerify()
	var refunded int64
	var status string
	if err := verify.Table("payment_order").Where("id = ?", execPayOrdID).
		Select("refunded_cents, status").Row().Scan(&refunded, &status); err != nil {
		t.Fatalf("读支付单失败：%v", err)
	}
	if refunded != 4000 {
		t.Errorf("已退金额 = %d，期望 4000——同一笔退款被并发入账两次", refunded)
	}
	if status != "partial_refunded" {
		t.Errorf("支付单状态 = %q，期望 partial_refunded（10000 退 4000 未退完）", status)
	}
	var receipts int64
	if err := verify.Table("refund_success_receipt").Where("refund_record_id = ?", refundID).
		Count(&receipts).Error; err != nil {
		t.Fatalf("统计退款回执失败：%v", err)
	}
	if receipts != 1 {
		t.Errorf("退款回执 %d 条，期望 1 条", receipts)
	}
}

// TestConcurrentApplyTwoRefundsAccumulates 钉住同一支付单上两笔退款并发累加。
//
// 4000 与 6000 两笔退款并发成功，累加必须精确等于 10000，
// 支付单随之置为 refunded。少加或多加都是资损。
func TestConcurrentApplyTwoRefundsAccumulates(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	const idA, idB = uint64(43011), uint64(43012)
	const noA, noB = "R-EXEC-A", "R-EXEC-B"

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupExecutableRefund(t, janitor, []uint64{idA, idB}) })
	cleanupExecutableRefund(t, setup, []uint64{idA, idB})
	seedExecutableRefund(t, setup)
	execRefund(t, setup, idA, noA, 4000)
	execRefund(t, setup, idB, noB, 6000)

	p1, close1 := testConn(t)
	defer close1()
	p2, close2 := testConn(t)
	defer close2()

	gate := newLockGate()
	armOnLock(t, p1, gate, "test:apply_lock2", "payment_order")
	defer gate.pass()

	e1 := RefundExecutor{DB: p1}
	e2 := RefundExecutor{DB: p2}
	done1 := make(chan error, 1)
	go func() {
		done1 <- e1.apply(context.Background(), execRecord(idA, noA, 4000),
			execRequest(noA, 4000), execResult(noA, 4000, "WR-A"))
	}()
	gate.await(t, "第一笔入账")

	trying := armOnLockAttempt(t, p2, "test:apply_attempt2", "payment_order")
	done2 := make(chan error, 1)
	go func() {
		done2 <- e2.apply(context.Background(), execRecord(idB, noB, 6000),
			execRequest(noB, 6000), execResult(noB, 6000, "WR-B"))
	}()

	awaitLockAttempt(t, trying, "第二笔退款入账")

	gate.pass()
	if err := <-done1; err != nil {
		t.Fatalf("第一笔入账失败：%v", err)
	}
	if err := <-done2; err != nil {
		t.Fatalf("第二笔入账失败：%v", err)
	}

	verify, closeVerify := testConn(t)
	defer closeVerify()
	var refunded int64
	var status string
	if err := verify.Table("payment_order").Where("id = ?", execPayOrdID).
		Select("refunded_cents, status").Row().Scan(&refunded, &status); err != nil {
		t.Fatalf("读支付单失败：%v", err)
	}
	if refunded != execPaid {
		t.Errorf("已退金额 = %d，期望 %d（4000 + 6000）——并发累加出错", refunded, execPaid)
	}
	if status != "refunded" {
		t.Errorf("支付单状态 = %q，期望 refunded（已退满 %d）", status, execPaid)
	}
	var chargeStatus string
	if err := verify.Table("charge_order").Where("id = ?", execChargeID).
		Select("status").Row().Scan(&chargeStatus); err != nil {
		t.Fatalf("读充电订单失败：%v", err)
	}
	if chargeStatus != "refunded" {
		t.Errorf("充电订单状态 = %q，期望 refunded", chargeStatus)
	}
}

// TestApplyRejectsRefundBeyondRemainingAmount 钉住退额上限校验。
//
// 支付单实付 10000、退满之后再来一笔 1000 的退款，必须判冲突。
// 没有这条，`refunded_cents` 会被推到超过实付——退的钱比收的多。
// 这条校验是唯一挡住它的东西，值得单独钉住。
func TestApplyRejectsRefundBeyondRemainingAmount(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	const idA, idB = uint64(43021), uint64(43022)
	const noA, noB = "R-EXEC-EX-A", "R-EXEC-EX-B"

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupExecutableRefund(t, janitor, []uint64{idA, idB}) })
	cleanupExecutableRefund(t, setup, []uint64{idA, idB})
	seedExecutableRefund(t, setup)
	execRefund(t, setup, idA, noA, 10000)
	execRefund(t, setup, idB, noB, 1000)

	e := RefundExecutor{DB: setup}
	if err := e.apply(context.Background(), execRecord(idA, noA, 10000),
		execRequest(noA, 10000), execResult(noA, 10000, "WR-EX-A")); err != nil {
		t.Fatalf("退满实付金额的那笔应成功：%v", err)
	}

	// 再退 1000：可退余额已归零，必须拒绝。
	if err := e.apply(context.Background(), execRecord(idB, noB, 1000),
		execRequest(noB, 1000), execResult(noB, 1000, "WR-EX-B")); err == nil {
		t.Error("可退余额已归零却仍接受了超额退款")
	}

	var refunded int64
	if err := setup.Table("payment_order").Where("id = ?", execPayOrdID).
		Select("refunded_cents").Row().Scan(&refunded); err != nil {
		t.Fatalf("读支付单失败：%v", err)
	}
	if refunded != execPaid {
		t.Errorf("已退金额 = %d，期望 %d（不得超过实付）", refunded, execPaid)
	}
	var receipts int64
	if err := setup.Table("refund_success_receipt").Where("refund_record_id = ?", idB).
		Count(&receipts).Error; err != nil {
		t.Fatalf("统计退款回执失败：%v", err)
	}
	if receipts != 0 {
		t.Errorf("被拒绝的超额退款却留下了 %d 条成功回执", receipts)
	}
}
