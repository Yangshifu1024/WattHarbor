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
	"gorm.io/gorm/clause"
)

// 真库集成测试的公共骨架：连接、并发闸门、加锁回调。
//
// 同一包内的多个集成测试共用这一份。并发验证必须用**两条独立连接**：
// 同一个 *sql.DB 池里的两个连接才是两个事务，复用同一条会让
// 「并发」退化成串行，测出来的是假象。

const testMySQLURLEnv = "TEST_MYSQL_URL"

// testConn 开一条独立的数据库连接。
//
// 环境变量缺失、连不上或 ping 失败一律 t.Fatal——不给「这次没测成」留出口。
// 跳过的测试在 go test 输出里和通过的一模一样，CI 没配库就会一路绿灯。
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

// lockGate 把一个事务卡在某条带 FOR UPDATE 的语句返回之后，
// 由主控用 channel 决定何时放行。每一步的先后都是确定的，
// 不靠起 N 个 goroutine 碰运气——那样的测试要么抓不到竞态，要么换台机器就偶发。
type lockGate struct {
	locked  chan struct{} // 指定行锁查询已返回，关闭后主控可启动竞争事务
	release chan struct{} // 主控释放持锁事务的通知，关闭前回调保持阻塞
	once    sync.Once     // 一次测试只捕获首次指定行锁
	passOne sync.Once     // 确保释放通道可被显式和 defer 重复关闭
}

// newLockGate 返回未触发、未放行的锁闸门；无 IO 或错误，由测试通过 await／pass 控制顺序。
func newLockGate() *lockGate {
	return &lockGate{locked: make(chan struct{}), release: make(chan struct{})}
}

// await 最多等待十五秒确认 what 对应锁已取得，超时终止测试；不放行持锁事务。
func (g *lockGate) await(t *testing.T, what string) {
	t.Helper()
	select {
	case <-g.locked:
	case <-time.After(15 * time.Second):
		t.Fatalf("%s 迟迟没有到达加锁语句——锁子句可能已改名或去掉", what)
	}
}

// armOnLockAttempt 注册指定表的 FOR UPDATE 请求通知，返回只读通道；不等待查询返回或阻塞回调。
// t 负责注销，db 必须是竞争连接，name 需唯一；用于确认旧快照普通读已完成并开始等待目标锁。
func armOnLockAttempt(t *testing.T, db *gorm.DB, name, table string) <-chan struct{} {
	t.Helper()
	trying := make(chan struct{})
	var once sync.Once
	if err := db.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement.Table == table {
			if _, ok := q.Statement.Clauses["FOR"]; ok {
				once.Do(func() { close(trying) })
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(name) })
	return trying
}

// awaitLockAttempt 等待 trying 通知，what 用于诊断；超时终止测试，不通过睡眠猜测竞争事务已开始。
func awaitLockAttempt(t *testing.T, trying <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-trying:
	case <-time.After(15 * time.Second):
		t.Fatalf("%s 未请求目标行锁", what)
	}
}

// pass 放行该事务。可重复调用，测试里既有显式放行也有 defer 兜底。
func (g *lockGate) pass() { g.passOne.Do(func() { close(g.release) }) }

// armOnLock 装回调：本次执行里第一次出现「带 FOR UPDATE 且命中 wantTable」的
// 查询时，在它返回之后把事务挂起，等主控放行。
func armOnLock(t *testing.T, db *gorm.DB, gate *lockGate, name, wantTable string) {
	t.Helper()
	err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if !strings.Contains(sql, "FOR UPDATE") || !strings.Contains(sql, "FROM `"+wantTable+"`") {
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

// TestSnapshotReadMissesCommitThatHappenedDuringLockWait 用两连接固定 RR 快照、并发提交及后续行锁顺序。
// 对照普通聚合与当前读，证明支付行锁不会刷新之前的快照；造数按测试支付身份清理。
func TestSnapshotReadMissesCommitThatHappenedDuringLockWait(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()

	const orderKey = uint64(81001)
	cleanup := func(db *gorm.DB) {
		db.Table("refund_record").Where("payment_order_id = ?", orderKey).Delete(nil)
		db.Table("payment_order").Where("id = ?", orderKey).Delete(nil)
	}
	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanup(janitor) })
	cleanup(setup)

	if err := setup.Table("payment_order").Create(map[string]any{
		"id": orderKey, "order_no": "P-SNAPSHOT-1", "biz_type": "charge", "biz_id": 1,
		"user_id": 81001, "pay_method": "balance", "total_cents": 10000,
		"paid_cents": 10000, "refunded_cents": 0, "status": "paid",
		"created_month": "2026-03-01",
	}).Error; err != nil {
		t.Fatalf("造支付单失败：%v", err)
	}

	p1, close1 := testConn(t)
	defer close1()
	p2, close2 := testConn(t)
	defer close2()

	// 事务一：普通读（建立快照）→ 拿行锁（此时会等事务二）。
	tx1 := p1.Begin()
	if tx1.Error != nil {
		t.Fatalf("事务一开启失败：%v", tx1.Error)
	}
	defer tx1.Rollback()

	var method string
	if err := tx1.Table("payment_order").Where("id = ?", orderKey).Pluck("pay_method", &method).Error; err != nil {
		t.Fatalf("普通读失败：%v", err)
	}

	tx2 := p2.Begin()
	if tx2.Error != nil {
		t.Fatalf("事务二开启失败：%v", tx2.Error)
	}
	// 事务二先提交一笔待处理退款——模拟并发的 ManualCreate。
	if err := tx2.Table("refund_record").Create(map[string]any{
		"refund_no": "R-SNAPSHOT-1", "payment_order_id": orderKey, "user_id": 81001,
		"biz_type": "charge", "biz_id": 1, "refund_cents": 4000,
		"status": "pending", "execution_policy": "automatic",
		"created_month": "2026-03-01",
	}).Error; err != nil {
		t.Fatalf("事务二插入退款单失败：%v", err)
	}
	if err := tx2.Commit().Error; err != nil {
		t.Fatalf("事务二提交失败：%v", err)
	}

	// 事务一现在才去拿行锁——它已经晚于事务二的提交。
	var locked struct{ ID uint64 }
	if err := tx1.Clauses(clause.Locking{Strength: "UPDATE"}).Table("payment_order").
		Where("id = ?", orderKey).Take(&locked).Error; err != nil {
		t.Fatalf("加锁读失败：%v", err)
	}

	// 快照读：看不到事务二刚插入的那 4000。
	var plainSum int64
	if err := tx1.Model(&RefundRecord{}).Select("COALESCE(SUM(refund_cents),0)").
		Where("payment_order_id = ? AND status IN ('pending','processing') AND deleted_at IS NULL", orderKey).
		Scan(&plainSum).Error; err != nil {
		t.Fatalf("快照聚合失败：%v", err)
	}
	// 加锁读：看得到。
	var lockedSum int64
	if err := tx1.Model(&RefundRecord{}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("COALESCE(SUM(refund_cents),0)").
		Where("payment_order_id = ? AND status IN ('pending','processing') AND deleted_at IS NULL", orderKey).
		Scan(&lockedSum).Error; err != nil {
		t.Fatalf("加锁聚合失败：%v", err)
	}

	if plainSum != 0 {
		t.Errorf("快照读本应看不到事务二的提交，实际读到 %d", plainSum)
	}
	if lockedSum != 4000 {
		t.Errorf("加锁读应看到事务二提交的 4000，实际读到 %d", lockedSum)
	}
}
