//go:build integration

package marketing

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"chargepilot/internal/platform/dbconn"
	"gorm.io/gorm"
)

// 真库并发测试用独立连接和锁闸门固定执行顺序。
// 先锁活动和模板，再统计发放量并写入，防止预算一份却并发发出两张券。

// lockGate 用来在拿到行锁后暂停一个事务。
type lockGate struct {
	locked  chan struct{} // 事务一到锁点就发信号
	release chan struct{} // 主控放行后才继续
	// once 只在首次到达锁点时通知并暂停。
	once sync.Once
	// passOne 保证放行通道最多关闭一次。
	passOne sync.Once
}

// newLockGate 造一个尚未触发的闸门。
func newLockGate() *lockGate {
	return &lockGate{locked: make(chan struct{}), release: make(chan struct{})}
}

// await 等待该事务到达锁点。
func (g *lockGate) await(t *testing.T, what string) {
	t.Helper()
	select {
	case <-g.locked:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s 迟迟没有到达加锁语句——锁子句可能已改名或去掉", what)
	}
}

// pass 幂等放行事务，退出清理可再次调用。
func (g *lockGate) pass() { g.passOne.Do(func() { close(g.release) }) }

// testConn 为并发事务打开独立连接，避免共用单连接导致串行执行。
func testConn(t *testing.T) *sqlPair {
	t.Helper()
	raw := os.Getenv(testMySQLURLEnv)
	if raw == "" {
		t.Fatalf("%s 未设置。真库集成测试不提供跳过路径。", testMySQLURLEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sqlDB, err := dbconn.Open(ctx, raw)
	if err != nil {
		t.Fatalf("建立独立连接失败：%v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	orm, err := dbconn.WrapGORM(sqlDB)
	if err != nil {
		t.Fatalf("包装 GORM 失败：%v", err)
	}
	return &sqlPair{sqlDB: sqlDB, orm: orm}
}

// cleanupSeed 清理本测试提交的预置数据；两个事务须读取已提交规则，不能只靠外层回滚清理。
func cleanupSeed(t *testing.T, db *gorm.DB, ruleIDs, couponIDs []uint64) {
	t.Helper()
	for _, id := range couponIDs {
		if err := db.Table("coupon_grant_request").
			Where("coupon_id = ?", id).Delete(nil).Error; err != nil {
			t.Errorf("清理幂等凭据 %d 失败：%v", id, err)
		}
		if err := db.Table("coupon_grant").Where("coupon_id = ?", id).Delete(nil).Error; err != nil {
			t.Errorf("清理发放记录 %d 失败：%v", id, err)
		}
		if err := db.Table("coupon_activity_rule").Where("coupon_id = ?", id).Delete(nil).Error; err != nil {
			t.Errorf("清理活动规则券 %d 失败：%v", id, err)
		}
		if err := db.Table("coupon").Where("id = ?", id).Delete(nil).Error; err != nil {
			t.Errorf("清理券 %d 失败：%v", id, err)
		}
	}
	for _, id := range ruleIDs {
		if err := db.Table("coupon_activity_rule").Where("id = ?", id).Delete(nil).Error; err != nil {
			t.Errorf("清理活动规则 %d 失败：%v", id, err)
		}
	}
}

// sqlPair 将同一独立连接池与 GORM 配对，测试结束后关闭。
type sqlPair struct {
	// sqlDB 是并发参与者的独立连接池。
	sqlDB *sql.DB
	// orm 是该连接池的 GORM 入口。
	orm *gorm.DB
}

// armOnce 在首次 FOR UPDATE 返回后暂停事务，等待主控放行。
func (p *sqlPair) armOnce(t *testing.T, gate *lockGate, name string) {
	t.Helper()
	err := p.orm.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if !strings.Contains(tx.Statement.SQL.String(), "FOR UPDATE") {
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

// TestConcurrentGrantsRespectBudgetUnderRowLock 验证一份预算下第二事务等待行锁，首个发放提交后发现预算已用尽。
func TestConcurrentGrantsRespectBudgetUnderRowLock(t *testing.T) {
	setup := testDB(t)
	tx := txRollback(t, setup)
	seedCoupon(t, tx, 2001, 24, nil)
	seedRule(t, tx, 1, TriggerFirstRecharge, hours(-1), hours(1), 0,
		withCoupon(2001), withMaxGrants(1))
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("预置数据提交失败：%v", err)
	}
	// 预置数据须提交让两连接可见，结束后单独清理。
	janitor := testConn(t)
	t.Cleanup(func() { cleanupSeed(t, janitor.orm, []uint64{1}, []uint64{2001}) })

	// 两条独立连接，各自开事务。
	p1, p2 := testConn(t), testConn(t)
	gate1 := newLockGate()
	p1.armOnce(t, gate1, "test:gate1")
	defer gate1.pass()

	tx1 := p1.orm.Begin()
	if tx1.Error != nil {
		t.Fatalf("事务一开启失败：%v", tx1.Error)
	}
	defer tx1.Rollback()

	// outcome 汇总并发发券结果，提交和回滚由测试控制。
	type outcome struct {
		// res 是发放结果；返回成功不代表外层事务已提交。
		res []ActivityResult
		err error
	}
	done1 := make(chan outcome, 1)
	go func() {
		res, err := (ActivityService{}).ApplyInTx(tx1, ActivityEvent{
			TriggerType: TriggerFirstRecharge, UserID: 8001,
			EventKey: "recharge:R-1", Now: fixedNow,
		})
		done1 <- outcome{res, err}
	}()

	// 等事务一确实拿到了行锁。
	gate1.await(t, "事务一")

	// 事务一仍持活动行锁，事务二须在数据库等待。
	tx2 := p2.orm.Begin()
	if tx2.Error != nil {
		t.Fatalf("事务二开启失败：%v", tx2.Error)
	}
	defer tx2.Rollback()

	done2 := make(chan outcome, 1)
	go func() {
		res, err := (ActivityService{}).ApplyInTx(tx2, ActivityEvent{
			TriggerType: TriggerFirstRecharge, UserID: 8002,
			EventKey: "recharge:R-2", Now: fixedNow,
		})
		done2 <- outcome{res, err}
	}()

	// 等待事务二尝试取锁，期间不能返回发放结果。
	select {
	case got := <-done2:
		t.Fatalf("事务一仍持锁时事务二就返回了（结果 %+v，错误 %v）——行锁没有串行化并发发放", got.res, got.err)
	case <-time.After(2 * time.Second):
		// 预期：被行锁挡住。
	}

	// 放行事务一，让它发券并提交。
	gate1.pass()
	first := <-done1
	if first.err != nil {
		t.Fatalf("事务一发券失败：%v", first.err)
	}
	if len(first.res) != 1 {
		t.Fatalf("事务一应拿到预算内的那一张，实际 %+v", first.res)
	}
	if err := tx1.Commit().Error; err != nil {
		t.Fatalf("事务一提交失败：%v", err)
	}

	// 事务二此时才拿到锁，重数时应看到预算已用尽。
	second := <-done2
	if second.err != nil {
		t.Fatalf("事务二因预算耗尽而跳过不该报错：%v", second.err)
	}
	if len(second.res) != 0 {
		t.Errorf("预算 1 份已被事务一用掉，事务二却拿到 %+v——发生了超发", second.res)
	}
	if err := tx2.Rollback().Error; err != nil {
		t.Fatalf("事务二回滚失败：%v", err)
	}

	// 用一条干净的连接核对最终落库结果。
	verify := testConn(t)
	var total int64
	if err := verify.orm.Table("coupon_grant").Count(&total).Error; err != nil {
		t.Fatalf("统计发放记录失败：%v", err)
	}
	if total != 1 {
		t.Errorf("最终发出 %d 张券，预算上限是 1 张", total)
	}
}

// TestConcurrentGrantsBothSucceedWhenBudgetAllows 验证预算充足时两个用户依次取得行锁，都能拿到券。
func TestConcurrentGrantsBothSucceedWhenBudgetAllows(t *testing.T) {
	setup := testDB(t)
	tx := txRollback(t, setup)
	seedCoupon(t, tx, 2002, 24, nil)
	seedRule(t, tx, 1, TriggerFirstRecharge, hours(-1), hours(1), 0,
		withCoupon(2002), withMaxGrants(5))
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("预置数据提交失败：%v", err)
	}
	janitor := testConn(t)
	t.Cleanup(func() { cleanupSeed(t, janitor.orm, []uint64{1}, []uint64{2002}) })

	p1, p2 := testConn(t), testConn(t)
	gate1 := newLockGate()
	p1.armOnce(t, gate1, "test:gate3")
	defer gate1.pass()

	tx1, tx2 := p1.orm.Begin(), p2.orm.Begin()
	if tx1.Error != nil || tx2.Error != nil {
		t.Fatalf("开启事务失败：%v %v", tx1.Error, tx2.Error)
	}
	defer tx1.Rollback()
	defer tx2.Rollback()

	// outcome 汇总并发发券结果，提交和回滚由测试控制。
	type outcome struct {
		// res 是发放结果；返回成功不代表外层事务已提交。
		res []ActivityResult
		err error
	}
	done1 := make(chan outcome, 1)
	done2 := make(chan outcome, 1)
	go func() {
		res, err := (ActivityService{}).ApplyInTx(tx1, ActivityEvent{
			TriggerType: TriggerFirstRecharge, UserID: 8101,
			EventKey: "recharge:R-A", Now: fixedNow,
		})
		done1 <- outcome{res, err}
	}()
	gate1.await(t, "事务一")

	go func() {
		res, err := (ActivityService{}).ApplyInTx(tx2, ActivityEvent{
			TriggerType: TriggerFirstRecharge, UserID: 8102,
			EventKey: "recharge:R-B", Now: fixedNow,
		})
		done2 <- outcome{res, err}
	}()

	gate1.pass()

	// 先等待并提交事务一释放锁，再等事务二；同时等两个结果会让事务二一直阻塞。
	first := <-done1
	if err := tx1.Commit().Error; err != nil {
		t.Fatalf("事务一提交失败：%v", err)
	}
	second := <-done2
	if err := tx2.Rollback().Error; err != nil {
		t.Fatalf("事务二回滚失败：%v", err)
	}

	if first.err != nil || second.err != nil {
		t.Fatalf("预算充足时并发发放不应报错：%v / %v", first.err, second.err)
	}
	if len(first.res) != 1 || len(second.res) != 1 {
		t.Fatalf("预算 5 份、两个用户并发，都应拿到券，实际 %+v 与 %+v", first.res, second.res)
	}
}
