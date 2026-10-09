//go:build integration

package settlement

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"chargepilot/internal/platform/dbconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 真库测试共用连接、并发闸门和锁回调；两个并发事务必须使用独立连接。

const testMySQLURLEnv = "TEST_MYSQL_URL"

// testConn 打开独立数据库连接；配置缺失或连接失败立即终止测试，不跳过验收。
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

// lockGate 在 FOR UPDATE 返回后暂停事务，由 channel 控制放行，确保并发顺序可重复。
type lockGate struct {
	locked  chan struct{} // 指定行锁查询已返回，关闭后主控可启动竞争事务
	release chan struct{} // 主控释放持锁事务的通知，关闭前回调保持阻塞
	once    sync.Once     // 一次测试只捕获首次指定行锁
	passOne sync.Once     // 确保释放通道可被显式和 defer 重复关闭
}

// newLockGate 创建等待和放行均未触发的锁闸门。
func newLockGate() *lockGate {
	return &lockGate{locked: make(chan struct{}), release: make(chan struct{})}
}

// await 最多等待十五秒确认指定锁已取得；超时终止测试。
func (g *lockGate) await(t *testing.T, what string) {
	t.Helper()
	select {
	case <-g.locked:
	case <-time.After(15 * time.Second):
		t.Fatalf("%s 迟迟没有到达加锁语句——锁子句可能已改名或去掉", what)
	}
}

// armOnLockAttempt 在竞争连接请求指定表的 FOR UPDATE 时发出通知，测试结束后注销。
// 通知表示已开始请求锁，不表示已取得锁。
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

// awaitLockAttempt 等待竞争事务开始请求锁；超时终止测试。
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

// armOnLock 在首次匹配目标表的 FOR UPDATE 返回后暂停事务，等待主控放行。
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
	var locked struct {
		// ID 仅用于确认支付锁查询命中目标行。
		ID uint64
	}
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
