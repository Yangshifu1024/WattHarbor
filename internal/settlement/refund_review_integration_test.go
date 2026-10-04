//go:build integration

package settlement

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

// 人工退款并发验收固定支付行锁的取得顺序，核对等待后的在途当前读不超原实付。

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
}

func cleanupRefundableOrder(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, q := range []struct {
		table string // 本测试拥有的持久表
		where string // 本测试退款或支付身份
		args  []any  // 筛选值
	}{
		{"refund_record", "payment_order_id = ?", []any{refundPayOrdID}},
		{"manual_refund_request", "request_id LIKE ?", []any{"MR-CONC-%"}},
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

// TestConcurrentManualRefundsDoNotExceedPaidAmount 钉住并发退款申请不超额。
//
// 实付 10000，两笔各 6000 各自都"看起来"可退，合计却是 12000。
// 单笔顺序提交时第二笔必然被 `pending > paid - refunded - amount` 拦下；
// 只有并发时才会双双通过——这正是快照读造成的。
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
	type outcome struct {
		created bool   // 是否实际新建原退款
		no      string // 原稳定退款单号
		err     error  // 原事务及建单结果
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
	// 落库的才是真相：两笔都插入过就是超发。
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
