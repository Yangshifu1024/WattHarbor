//go:build integration

package settlement

import (
	"chargepilot/internal/testfixture"
	"context"
	"fmt"
	"github.com/google/uuid"
	"testing"
	"time"

	pay "chargepilot/internal/payment"
	"gorm.io/gorm"
)

// 真库测试覆盖 SUCCESS 退款入账的幂等和并发累加。
// 资金按钱包分摊行→支付→退款加锁，累计已退金额须读取锁后的当前值。

const (
	execUserID   = uint64(43001)
	execPayOrdID = uint64(43001)
	execChargeID = uint64(43001)
	execPaid     = int64(10000)
)

// execRefund 构造 processing 的充电自动退款，只涉及支付、退款和充电订单，不走钱包分摊。
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

// seedExecutableRefund 创建实付 10000 分的支付单和 refunding 充电订单；调用方负责固定身份独占与清理。
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

// cleanupExecutableRefund 清理本测试退款、成功回执、充电和支付记录。
// Outbox 仅删除同时匹配退款来源及支付身份的事件，逐项报告失败。
func cleanupExecutableRefund(t *testing.T, db *gorm.DB, refundIDs []uint64) {
	t.Helper()
	// 仅清理由本测试退款单号生成的事件，避免删除其他包的开发或并发测试数据。
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

// execRecord 构造关联固定支付、用户和订单的 processing 退款，金额单位为分。
func execRecord(id uint64, refundNo string, cents int64) RefundRecord {
	return RefundRecord{
		ID: id, RefundNo: refundNo, PaymentOrderID: execPayOrdID, UserID: execUserID,
		BizType: "charge", BizID: execChargeID, RefundCents: cents, Status: "processing",
	}
}

// execRequest 构造固定商户单号、交易号和退款号的渠道请求，实付总额固定 10000 分。
func execRequest(refundNo string, cents int64) pay.RefundRequest {
	return pay.RefundRequest{
		RefundNo: refundNo, MerchantOrderNo: "P-EXEC-1",
		TransactionID: "SIM4200000002", TotalCents: execPaid, RefundCents: cents,
	}
}

// execResult 构造 SUCCESS 渠道回执和 UTC 成功时间供真库入账；不会实际请求渠道。
func execResult(refundNo string, cents int64, refundID string) pay.RefundResult {
	return pay.RefundResult{
		RefundNo: refundNo, MerchantOrderNo: "P-EXEC-1", TransactionID: "SIM4200000002",
		TotalCents: execPaid, RefundCents: cents, RefundID: refundID, Status: "SUCCESS",
		SuccessAt: time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC),
	}
}

// TestConcurrentApplySameRefundCreditsOnce 验证同一退款并发入账只增加一次已退金额、只保存一条成功回执。
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

// TestConcurrentApplyTwoRefundsAccumulates 验证同一支付的 4000 分和 6000 分退款并发成功后累计 10000 分，并变为 refunded。
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

// TestApplyRejectsRefundBeyondRemainingAmount 验证实付 10000 分已全部退完后，再退 1000 分必须报冲突。
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

// TestAbnormalWalletRefundKeepsReservationUntilTerminal 使用真实资金表验证异常不是失败，最终结果只结算一次。
func TestAbnormalWalletRefundKeepsReservationUntilTerminal(t *testing.T) {
	for _, terminal := range []pay.ChannelRefundStatus{pay.ChannelRefundStatusSuccess, pay.ChannelRefundStatusClosed} {
		t.Run(string(terminal), func(t *testing.T) {
			db, closeDB := testConn(t)
			defer closeDB()
			tx := db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			id := testfixture.NewID()
			no := fmt.Sprintf("ABN-%d", id)
			for _, seed := range []struct {
				table string
				row   map[string]any
			}{
				{"wallet_account", map[string]any{"id": id, "user_id": id, "balance_cents": 1000, "frozen_cents": 400}},
				{"payment_order", map[string]any{"id": id, "order_no": "P-" + no, "user_id": id, "biz_type": "wallet_recharge", "biz_id": 0, "pay_method": "wechat", "status": "paid", "total_cents": 1000, "paid_cents": 1000, "wechat_transaction_id": "TX-" + no, "created_month": "2026-10-01"}},
				{"refund_record", map[string]any{"id": id, "refund_no": no, "payment_order_id": id, "user_id": id, "biz_type": "wallet_recharge", "biz_id": 0, "refund_cents": 400, "status": "processing", "execution_policy": "automatic", "created_month": "2026-10-01"}},
				{"wallet_refund_part", map[string]any{"refund_record_id": id, "request_id": uuid.NewString(), "wallet_account_id": id, "amount_cents": 400}},
			} {
				if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
					t.Fatal(err)
				}
			}
			r := RefundRecord{ID: id, RefundNo: no, PaymentOrderID: id, UserID: id, BizType: "wallet_recharge", RefundCents: 400, Status: "processing"}
			request := pay.RefundRequest{RefundNo: no, MerchantOrderNo: "P-" + no, TransactionID: "TX-" + no, TotalCents: 1000, RefundCents: 400}
			result := pay.RefundResult{RefundNo: no, MerchantOrderNo: request.MerchantOrderNo, TransactionID: request.TransactionID, TotalCents: 1000, RefundCents: 400, RefundID: "WX-" + no, Status: pay.ChannelRefundStatusAbnormal}
			executor := RefundExecutor{DB: tx}
			for range 2 {
				if err := executor.apply(t.Context(), r, request, result); err != nil {
					t.Fatal(err)
				}
			}
			var account struct{ BalanceCents, FrozenCents int64 }
			var part struct{ Settled bool }
			var current struct {
				Status         string
				WechatRefundID string
				FailureReason  string
			}
			if err := tx.Table("wallet_account").Where("id=?", id).Take(&account).Error; err != nil {
				t.Fatal(err)
			}
			if err := tx.Table("wallet_refund_part").Where("refund_record_id=?", id).Take(&part).Error; err != nil {
				t.Fatal(err)
			}
			if err := tx.Table("refund_record").Where("id=?", id).Take(&current).Error; err != nil {
				t.Fatal(err)
			}
			if account.BalanceCents != 1000 || account.FrozenCents != 400 || part.Settled || current.Status != "processing" || current.WechatRefundID != result.RefundID || current.FailureReason == "" {
				t.Fatalf("abnormal changed funds: wallet=%+v part=%+v refund=%+v", account, part, current)
			}
			result.Status = terminal
			result.SuccessAt = time.Now().UTC()
			for range 2 {
				if err := executor.apply(t.Context(), r, request, result); err != nil {
					t.Fatal(err)
				}
			}
			// 终态已落库后迟到的异常不能复活退款或恢复旧冻结。
			result.Status = pay.ChannelRefundStatusAbnormal
			if err := executor.apply(t.Context(), r, request, result); err != nil {
				t.Fatal(err)
			}
			if err := tx.Table("refund_record").Where("id=?", id).Take(&current).Error; err != nil {
				t.Fatal(err)
			}
			wantStatus := "failed"
			if terminal == pay.ChannelRefundStatusSuccess {
				wantStatus = "success"
			}
			if current.Status != wantStatus {
				t.Fatalf("late abnormal revived refund: %+v", current)
			}
			if err := tx.Table("wallet_account").Where("id=?", id).Take(&account).Error; err != nil {
				t.Fatal(err)
			}
			var ledger int64
			if err := tx.Table("wallet_txn").Where("user_id=?", id).Count(&ledger).Error; err != nil {
				t.Fatal(err)
			}
			wantBalance, wantLedger := int64(1000), int64(0)
			if terminal == pay.ChannelRefundStatusSuccess {
				wantBalance, wantLedger = 600, 1
			}
			if account.BalanceCents != wantBalance || account.FrozenCents != 0 || ledger != wantLedger {
				t.Fatalf("terminal %s wallet=%+v ledger=%d", terminal, account, ledger)
			}
		})
	}
}
