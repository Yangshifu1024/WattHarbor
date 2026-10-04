//go:build integration

package http

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"gorm.io/gorm"
)

// 充电结束回执的真库并发测试。
//
// 这是整条跨库链路上离钱最近的一环：gateway 采集到结束帧 → worker 同步到
// central → central 落结束回执、结束计量、释放端口占用、派发计费。
// worker 在「central 已确认、gateway 端口释放失败」时会整条重放，
// 所以 central 这头**必须幂等**：重复投递要么识别为重放，要么判冲突，
// 但绝不能重复落计量、重复释放端口、重复派发计费。
//
// 这段的安全性依赖事务显式声明的 READ COMMITTED（order_end_result.go:122）。
// 在 REPEATABLE READ 下，重复投递的那个事务会在 charge_end_receipt 上读到
// 快照旧值（看不到前一事务刚落的回执），于是跳过重放分支、落到订单状态检查，
// 判成冲突而不是重放——worker 因此永远等不到 200，端口一直占着。
// 把那行 TxOptions 删掉，本测试立刻变红；这一点单测完全看不出来。

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
		t.Fatalf("%s 迟迟没有到达加锁语句", what)
	}
}

func (g *lockGate) pass() { g.passOne.Do(func() { close(g.release) }) }

// armOnOrderLock 装回调：第一次锁定 charge_order 行后挂起该事务。
func armOnOrderLock(t *testing.T, db *gorm.DB, gate *lockGate, name string) {
	t.Helper()
	err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		// 必须按 FROM 子句匹配表名，不能只匹配 "charge_order"：
		// card_charge 那条查询的 WHERE 列名是 charge_order_id，含这个子串，
		// 闸门会挂在它上面——而 card_charge 没有行时 FOR UPDATE 只取间隙锁，
		// 挡不住第二个事务，测试就会误判成「没有串行化」。
		if strings.Contains(sql, "FOR UPDATE") && strings.Contains(sql, "FROM `charge_order`") {
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
	endOrderID  = uint64(61001)
	endPortID   = uint64(61001)
	endStartCmd = "11111111-1111-4111-8111-111111111111"
	endStopCmd  = "22222222-2222-4222-8222-222222222222"
)

func endResult() EndResult {
	return EndResult{
		OrderNo: "C-END-1", ChargeOrderID: endOrderID,
		StartCommandID: endStartCmd, StopCommandID: endStopCmd,
		DeviceID: "D-END-1", PortNo: 1, PortID: endPortID,
		Meter: charging.EndMeter{
			ChargedWh: 12000, ChargedSeconds: 3600,
			EndedAt:    time.Date(2026, 3, 4, 9, 30, 0, 0, time.UTC),
			StopReason: 1,
		},
	}
}

func seedChargingOrder(t *testing.T, db *gorm.DB) {
	t.Helper()
	started := time.Date(2026, 3, 4, 8, 0, 0, 0, time.UTC)
	if err := db.Table("charge_order").Create(map[string]any{
		"id": endOrderID, "order_no": "C-END-1", "user_id": 61001,
		"device_id": "D-END-1", "port_no": 1, "status": "charging",
		"payment_status": "paid", "started_at": started,
		"total_cents": 1000, "electric_cents": 1000, "service_cents": 0,
		"created_month": "2026-03-01",
	}).Error; err != nil {
		t.Fatalf("造充电订单失败：%v", err)
	}
	if err := db.Table("charge_start_receipt").Create(map[string]any{
		"charge_order_id": endOrderID, "command_id": endStartCmd, "success": true, "port_id": endPortID,
	}).Error; err != nil {
		t.Fatalf("造起充回执失败：%v", err)
	}
	if err := db.Table("active_port_charge").Create(map[string]any{
		"port_id": endPortID, "device_id": "D-END-1", "port_no": 1,
		"charge_order_id": endOrderID, "user_id": 61001, "started_at": started,
	}).Error; err != nil {
		t.Fatalf("造端口占用失败：%v", err)
	}
}

func cleanupChargingOrder(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, q := range []struct{ table, where string }{
		{"charge_billing_job", "charge_order_id = ?"},
		{"charge_event_log", "charge_order_id = ?"},
		{"charge_end_receipt", "charge_order_id = ?"},
		{"charge_start_receipt", "charge_order_id = ?"},
		{"active_port_charge", "charge_order_id = ?"},
		{"charge_order", "id = ?"},
	} {
		if err := db.Table(q.table).Where(q.where, endOrderID).Delete(nil).Error; err != nil {
			t.Errorf("清理 %s 失败：%v", q.table, err)
		}
	}
	if err := db.Table("event_outbox").Where("event_id = ?", endStopCmd).Delete(nil).Error; err != nil {
		t.Errorf("清理发件箱失败：%v", err)
	}
}

// TestConcurrentEndResultAppliesOnce 钉住并发重复投递只结算一次。
//
// 两笔完全相同的结束回执并发到达：一笔真正结算，另一笔必须被识别为重放。
// 结算侧只应留下：一条结束回执、一条事件日志、一次计费派发、一条发件箱。
func TestConcurrentEndResultAppliesOnce(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupChargingOrder(t, janitor) })
	cleanupChargingOrder(t, setup)
	seedChargingOrder(t, setup)

	p1, close1 := testConn(t)
	defer close1()
	p2, close2 := testConn(t)
	defer close2()

	gate := newLockGate()
	armOnOrderLock(t, p1, gate, "test:end_lock")
	defer gate.pass()

	type outcome struct {
		replayed bool
		err      error
	}
	done1 := make(chan outcome, 1)
	go func() {
		r, err := EndResultStore{DB: p1}.Apply(context.Background(), endResult())
		done1 <- outcome{r, err}
	}()
	gate.await(t, "第一笔结束回执")

	done2 := make(chan outcome, 1)
	go func() {
		r, err := EndResultStore{DB: p2}.Apply(context.Background(), endResult())
		done2 <- outcome{r, err}
	}()

	select {
	case got := <-done2:
		t.Fatalf("第一笔仍持订单锁时第二笔就返回了（replayed=%v err=%v）——结算没有串行化", got.replayed, got.err)
	case <-time.After(2 * time.Second):
	}

	gate.pass()
	first, second := <-done1, <-done2
	if first.err != nil {
		t.Fatalf("第一笔结算失败：%v", first.err)
	}
	if second.err != nil {
		t.Fatalf("重复投递应被识别为重放，却报错 %v——worker 会因此永远不释放端口", second.err)
	}
	if !second.replayed {
		t.Error("第二笔未标记为重放，调用方无法区分首次与重放")
	}

	verify, closeVerify := testConn(t)
	defer closeVerify()
	for _, c := range []struct {
		name, table, where string
		want               int64
	}{
		{"结束回执", "charge_end_receipt", "charge_order_id = ?", 1},
		{"事件日志", "charge_event_log", "charge_order_id = ?", 1},
		{"计费派发", "charge_billing_job", "charge_order_id = ?", 1},
		{"发件箱", "event_outbox", "event_id = ?", 1},
		{"端口占用（应已释放）", "active_port_charge", "charge_order_id = ?", 0},
	} {
		arg := any(endOrderID)
		if c.table == "event_outbox" {
			arg = endStopCmd
		}
		var n int64
		if err := verify.Table(c.table).Where(c.where, arg).Count(&n).Error; err != nil {
			t.Fatalf("统计 %s 失败：%v", c.name, err)
		}
		if n != c.want {
			t.Errorf("%s = %d 条，期望 %d 条——重复投递造成了重复结算", c.name, n, c.want)
		}
	}
	var status string
	if err := verify.Table("charge_order").Where("id = ?", endOrderID).
		Select("status").Row().Scan(&status); err != nil {
		t.Fatalf("读充电订单失败：%v", err)
	}
	if status != "completed" {
		t.Errorf("订单状态 = %q，期望 completed", status)
	}
}

// TestSequentialReplayIsIdempotent 钉住顺序重放同样幂等。
//
// worker 在「central 已确认、端口释放失败」时会重放整条链路，
// 这是最常见的重放来源，必须走重放路径而不是冲突。
func TestSequentialReplayIsIdempotent(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupChargingOrder(t, janitor) })
	cleanupChargingOrder(t, setup)
	seedChargingOrder(t, setup)

	store := EndResultStore{DB: setup}
	replayed, err := store.Apply(context.Background(), endResult())
	if err != nil {
		t.Fatalf("首次结算失败：%v", err)
	}
	if replayed {
		t.Error("首次结算不该被标记为重放")
	}
	replayed, err = store.Apply(context.Background(), endResult())
	if err != nil {
		t.Fatalf("顺序重放应被接受，却报错：%v", err)
	}
	if !replayed {
		t.Error("顺序重放未被识别为重放")
	}
}

// TestDifferentMeterForSameOrderConflicts 钉住同单不同计量必须判冲突。
//
// 重复投递的判定是**逐字段比对**：电量、时长、停止原因、分段、结束时刻、
// 停止指令号任何一项对不上，都说明来的是另一份事实，绝不能沿用首次结果。
func TestDifferentMeterForSameOrderConflicts(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupChargingOrder(t, janitor) })
	cleanupChargingOrder(t, setup)
	seedChargingOrder(t, setup)

	store := EndResultStore{DB: setup}
	if _, err := store.Apply(context.Background(), endResult()); err != nil {
		t.Fatalf("首次结算失败：%v", err)
	}

	cases := map[string]func(*EndResult){
		"电量不同":    func(r *EndResult) { r.Meter.ChargedWh = 99000 },
		"时长不同":    func(r *EndResult) { r.Meter.ChargedSeconds = 7200 },
		"停止原因不同":  func(r *EndResult) { r.Meter.StopReason = 9 },
		"结束时刻不同":  func(r *EndResult) { r.Meter.EndedAt = r.Meter.EndedAt.Add(time.Minute) },
		"停止指令号不同": func(r *EndResult) { r.StopCommandID = "33333333-3333-4333-8333-333333333333" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			other := endResult()
			mutate(&other)
			if _, err := store.Apply(context.Background(), other); err == nil {
				t.Error("内容不一致的结束回执被沿用了首次结果")
			}
		})
	}
}
