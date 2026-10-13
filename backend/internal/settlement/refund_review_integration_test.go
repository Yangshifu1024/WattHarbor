//go:build integration

package settlement

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

// 真库测试固定支付锁顺序，验证等待后用当前读统计在途退款，累计不超过实付金额。

const (
	refundUserID   = uint64(41001)
	refundPaid     = int64(10000)
	refundOrderID  = uint64(41001)
	refundPayOrdID = uint64(41001)
	refundActorID  = uint64(9001)
)

// seedRefundableOrder 造一笔已支付、可退款的订单：实付 10000 分。
func seedRefundableOrder(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Table("payment_order").Create(map[string]any{
		"id": refundPayOrdID, "order_no": "P-REFUND-1", "biz_type": "charge", "biz_id": refundOrderID,
		"user_id": refundUserID, "pay_method": "wechat", "total_cents": refundPaid,
		"paid_cents": refundPaid, "refunded_cents": 0, "status": "paid",
		"wechat_transaction_id": "4200000001", "created_month": "2026-03-01",
	}).Error; err != nil {
		t.Fatalf("造支付单失败：%v", err)
	}
	if err := db.Table("charge_order").Create(map[string]any{
		"id": refundOrderID, "order_no": "C-REFUND-1", "user_id": refundUserID,
		"device_id": "D1", "port_no": 1, "status": "completed",
		"payment_status": "paid", "payment_order_id": refundPayOrdID,
		"total_cents": refundPaid, "electric_cents": refundPaid, "service_cents": 0,
		"created_month": "2026-03-01",
		"started_at":    time.Now().Add(-2 * time.Hour).UTC(), "ended_at": time.Now().Add(-time.Hour).UTC(),
	}).Error; err != nil {
		t.Fatalf("造充电订单失败：%v", err)
	}
	if err := db.Table("charge_fee_receipt").Create(map[string]any{
		"charge_order_id": refundOrderID, "calculation_no": "CALC-REFUND-1", "result_json": `{"total_cents":10000}`,
	}).Error; err != nil {
		t.Fatal(err)
	}

}

// cleanupRefundableOrder 清理固定支付身份的退款、MR-CONC 请求和订单，核对零残留；调用方负责身份独占。
func cleanupRefundableOrder(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, q := range []struct {
		table string // 本测试拥有的持久表
		where string // 本测试退款或支付身份
		args  []any  // 筛选值
	}{
		{"refund_record", "payment_order_id = ?", []any{refundPayOrdID}},
		{"manual_refund_request", "request_id LIKE ?", []any{"MR-CONC-%"}},
		{"charge_fee_receipt", "charge_order_id = ?", []any{refundOrderID}},
		{"charge_order", "id = ?", []any{refundOrderID}},
		{"payment_order", "id = ?", []any{refundPayOrdID}},
	} {
		if err := db.Table(q.table).Where(q.where, q.args...).Delete(nil).Error; err != nil {
			t.Errorf("清理 %s 失败：%v", q.table, err)
		}
		var n int64
		if err := db.Table(q.table).Where(q.where, q.args...).Count(&n).Error; err != nil || n != 0 {
			t.Errorf("remaining %s=%d err=%v", q.table, n, err)
		}
	}
}

// TestConcurrentManualRefundsDoNotExceedPaidAmount 验证实付 10000 分时，两笔各 6000 分的并发申请只能成功一笔。
// 支付锁后的在途金额必须用当前读，避免旧快照导致超额申请。
func TestConcurrentManualRefundsDoNotExceedPaidAmount(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupRefundableOrder(t, janitor) })
	cleanupRefundableOrder(t, setup)
	seedRefundableOrder(t, setup)

	p1, close1 := testConn(t)
	defer close1()
	p2, close2 := testConn(t)
	defer close2()

	gate := newLockGate()
	armOnLock(t, p1, gate, "test:refund_lock", "payment_order")
	defer gate.pass()

	store := RefundReviewRepo{}
	// outcome 汇总人工退款是否新建、退款号和错误。
	type outcome struct {
		created bool   // 是否实际新建退款
		no      string // 稳定退款单号
		err     error  // 事务及建单结果
	}
	mk := func(p *gorm.DB, requestID string) chan outcome {
		ch := make(chan outcome, 1)
		go func() {
			tx := p.WithContext(context.Background()).Begin()
			if tx.Error != nil {
				ch <- outcome{err: tx.Error}
				return
			}
			created, no, _, err := store.ManualCreate(context.Background(), tx, ManualRefundInput{
				ActorID: refundActorID, OrderID: refundOrderID,
				RequestID: requestID, AmountCents: 6000, Reason: "并发超额测试",
			})
			if err != nil {
				_ = tx.Rollback().Error
			} else {
				err = tx.Commit().Error
			}
			ch <- outcome{created, no, err}
		}()
		return ch
	}

	done1 := mk(p1, "MR-CONC-A")
	gate.await(t, "第一笔退款申请")
	trying := armOnLockAttempt(t, p2, "test:refund_attempt", "payment_order")
	done2 := mk(p2, "MR-CONC-B")

	awaitLockAttempt(t, trying, "第二笔退款申请")

	gate.pass()
	first, second := <-done1, <-done2

	if first.err != nil || !first.created || first.no == "" {
		t.Fatalf("first=%+v", first)
	}
	if !errors.Is(second.err, ErrRefundConflict) || second.created {
		t.Fatalf("second must conflict: %+v", second)
	}
	// 核对实际保存的记录，两笔都创建即为超额。
	verify, closeVerify := testConn(t)
	defer closeVerify()
	var rows int64
	var total int64
	if err := verify.Table("refund_record").Where("payment_order_id = ?", refundPayOrdID).
		Count(&rows).Error; err != nil {
		t.Fatalf("统计退款记录失败：%v", err)
	}
	if err := verify.Table("refund_record").Where("payment_order_id = ?", refundPayOrdID).
		Select("COALESCE(SUM(refund_cents),0)").Row().Scan(&total); err != nil {
		t.Fatalf("合计退款额失败：%v", err)
	}
	if total != 6000 {
		t.Errorf("待处理退款合计 %d 分，超过实付 %d 分（%d 条记录）", total, refundPaid, rows)
	}
	if rows != 1 {
		t.Errorf("并发的两笔申请都落了库（%d 条，合计 %d 分）", rows, total)
	}
}

// TestManualRefundRequiresSettledCharge 真实充电缺回执时拒绝且不留退款/请求；失败和取消仍可退款。
func TestManualRefundRequiresSettledCharge(t *testing.T) {
	for _, status := range []string{"completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			db, closeDB := testConn(t)
			defer closeDB()
			tx := db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			// 此夹具的支付/订单身份只在当前回滚事务内存在；不清理他人记录。
			seedRefundableOrder(t, tx)
			if err := tx.Table("charge_fee_receipt").Where("charge_order_id=?", refundOrderID).Delete(nil).Error; err != nil {
				t.Fatal(err)
			}
			if err := tx.Table("charge_order").Where("id=?", refundOrderID).Update("status", status).Error; err != nil {
				t.Fatal(err)
			}
			in := ManualRefundInput{ActorID: refundActorID, OrderID: refundOrderID, RequestID: "unsettled-" + status, AmountCents: refundPaid, Reason: "退款准入"}
			created, _, _, err := (RefundReviewRepo{}).ManualCreate(t.Context(), tx, in)
			if status == "completed" {
				if !errors.Is(err, ErrRefundNotSettled) || created {
					t.Fatalf("created=%v err=%v", created, err)
				}
				for _, table := range []string{"refund_record", "manual_refund_request"} {
					var n int64
					query := tx.Table(table)
					if table == "refund_record" {
						query = query.Where("payment_order_id=?", refundPayOrdID)
					} else {
						query = query.Where("request_id=?", in.RequestID)
					}
					if err := query.Count(&n).Error; err != nil || n != 0 {
						t.Fatalf("%s count=%d err=%v", table, n, err)
					}
				}
			} else if err != nil || !created {
				t.Fatalf("%s created=%v err=%v", status, created, err)
			}
		})
	}
}

// TestManualRefundReceiptUsesCurrentRead 锁前建立旧 RR 快照后，仍识别另一连接提交的费用回执。
func TestManualRefundReceiptUsesCurrentRead(t *testing.T) {
	writer, closeWriter := testConn(t)
	defer closeWriter()
	reader, closeReader := testConn(t)
	defer closeReader()
	cleanupRefundableOrder(t, writer)
	seedRefundableOrder(t, writer)
	defer cleanupRefundableOrder(t, writer)
	if err := writer.Table("charge_fee_receipt").Where("charge_order_id=?", refundOrderID).Delete(nil).Error; err != nil {
		t.Fatal(err)
	}
	old := oldFundingSnapshot(t, reader, "charge_order", "id", refundOrderID)
	defer old.Rollback()
	if err := writer.Table("charge_fee_receipt").Create(map[string]any{"charge_order_id": refundOrderID, "calculation_no": "CALC-CURRENT-1", "result_json": `{"total_cents":10000}`}).Error; err != nil {
		t.Fatal(err)
	}
	// 渠道缺单后的执行闸门同样须当前读，不能因调用方已有旧RR快照而误挡费用已提交的单。
	if err := (RefundExecutor{DB: old}).requireSettlementBeforeCreate(t.Context(), RefundRecord{PaymentOrderID: refundPayOrdID, BizType: "charge", BizID: refundOrderID}); err != nil {
		t.Fatalf("executor current fee read: %v", err)
	}
	created, _, _, err := (RefundReviewRepo{}).ManualCreate(t.Context(), old, ManualRefundInput{ActorID: refundActorID, OrderID: refundOrderID, RequestID: "current-fee-receipt", AmountCents: 100, Reason: "当前费用回执"})
	if err != nil || !created {
		t.Fatalf("current read created=%v err=%v", created, err)
	}
}
