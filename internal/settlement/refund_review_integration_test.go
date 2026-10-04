//go:build integration

package settlement

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

// 人工退款的真库并发测试：同一笔支付单上的两笔退款申请不得合计超额。
//
// 这段是"先锁支付单、再聚合已占用额度"的结构（refund_review_store.go:66 锁行，
// 第 96 行 SUM）。聚合读若是快照读，两笔并发申请会各自读到「未占用 0」，
// 一起通过 `pending > paid - refunded - amount` 校验，合计退超过实付。

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

// armOnPaymentOrderLock 装回调：第一次锁定 payment_order 行后挂起该事务。
func armOnPaymentOrderLock(t *testing.T, db *gorm.DB, gate *lockGate, name string) {
	t.Helper()
	err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if strings.Contains(sql, "FOR UPDATE") && strings.Contains(sql, "payment_order") {
			gate.once.Do(func() {
				close(gate.locked)
				<-gate.release
			})
		}
	})
	if err != nil {
		t.Fatalf("注册加锁回调失败：%v", err)
	}
}

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
		table, where string
		args         []any
	}{
		{"refund_record", "payment_order_id = ?", []any{refundPayOrdID}},
		{"manual_refund_request", "request_id LIKE ?", []any{"MR-CONC-%"}},
		{"charge_order", "id = ?", []any{refundOrderID}},
		{"payment_order", "id = ?", []any{refundPayOrdID}},
	} {
		if err := db.Table(q.table).Where(q.where, q.args...).Delete(nil).Error; err != nil {
			t.Errorf("清理 %s 失败：%v", q.table, err)
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
	armOnPaymentOrderLock(t, p1, gate, "test:refund_lock")
	defer gate.pass()

	store := ReviewStore{}
	type outcome struct {
		created bool
		no      string
		err     error
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
			} else if err := tx.Commit().Error; err != nil {
				err = err
			}
			ch <- outcome{created, no, err}
		}()
		return ch
	}

	done1 := mk(p1, "MR-CONC-A")
	gate.await(t, "第一笔退款申请")
	done2 := mk(p2, "MR-CONC-B")

	select {
	case got := <-done2:
		t.Fatalf("第一笔仍持支付单锁时第二笔就返回了（created=%v err=%v）——支付单行锁没有串行化退款申请", got.created, got.err)
	case <-time.After(2 * time.Second):
	}

	gate.pass()
	first, second := <-done1, <-done2

	succeeded := 0
	var errs []error
	for _, o := range []outcome{first, second} {
		if o.err != nil {
			errs = append(errs, o.err)
			continue
		}
		if o.created {
			succeeded++
		}
	}
	if succeeded > 1 {
		t.Errorf("实付 %d 分，两笔各 6000 分的申请都创建成功了（合计 %d，超发 %d）",
			refundPaid, int64(succeeded)*6000, int64(succeeded)*6000-refundPaid)
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
	if total > refundPaid {
		t.Errorf("待处理退款合计 %d 分，超过实付 %d 分（%d 条记录）", total, refundPaid, rows)
	}
	if rows > 1 {
		t.Errorf("并发的两笔申请都落了库（%d 条，合计 %d 分）", rows, total)
	}
	_ = errs
}
