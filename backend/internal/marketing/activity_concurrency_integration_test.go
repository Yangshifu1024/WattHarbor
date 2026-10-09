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

// 行锁并发：两个独立连接同时抢同一张活动规则。
//
// 这是本仓库最没有覆盖的一块。发券的临界区是「FOR UPDATE 锁行 → 数已发放量
// → 插入发放记录」三步（coupon_activity_repo.go:115-130）。锁一旦失效，
// 两个事务会同时数到 0 份、同时插入，活动预算 max_grants=1 就会发出 2 张。
//
// 真库能验这件事，但**不能靠起 N 个 goroutine 碰运气**——那样的测试要么
// 抓不到竞态，要么在别的机器上偶发失败，两头都不合格。这里用 gorm 回调
// 把事务一精确卡在「刚拿到行锁」的那一刻，再让事务二冲上去，
// 每一步的先后都是确定的。

// lockGate 用来在拿到行锁后暂停一个事务。
type lockGate struct {
	locked  chan struct{} // 事务一到锁点就发信号
	release chan struct{} // 主控放行后才继续
	// once 确保首次到达锁点的通知及挂起仅发生一次，后续查询不重复进入同一闸门。
	once sync.Once
	// passOne 确保 release 通道最多关闭一次，显式放行与退出清理可重复调用。
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

// pass 放行该事务。可重复调用——测试里既有显式放行也有 defer 兜底。
func (g *lockGate) pass() { g.passOne.Do(func() { close(g.release) }) }

// testConn 开一条**独立于其他测试**的数据库连接。
//
// 并发验证必须用两条独立连接：同一个 *sql.DB 池里的两个连接才是两个事务，
// 而复用同一条连接会让「并发」退化成串行，测出来的是假象。
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

// cleanupSeed 清掉并发测试提交出去的预置数据。
//
// 这两个测试必须真的提交预置数据（两个独立事务要能看到同一行规则），
// 事务回滚管不到，所以收尾自己删。ID 都取自各自的独占区间，互不干扰。
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

// sqlPair 组合独立测试 SQL 池与同池 ORM，供两个真实事务并发；池由创建测试的 Cleanup 关闭。
type sqlPair struct {
	// sqlDB 此并发参与者独立打开的 SQL 连接池，与 orm 共用连接资源。
	sqlDB *sql.DB
	// orm 同一 sqlDB 的 GORM 入口，供安装锁点回调和开启真实事务。
	orm *gorm.DB
}

// armOnce 装上回调：本次执行里第一次出现带 FOR UPDATE 的查询后，
// 就在它返回之后把事务挂起，等主控放行。
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

// TestConcurrentGrantsRespectBudgetUnderRowLock 钉住并发下不超发。
//
// 活动预算 1 份，两个用户同时触发。事务一先拿到行锁并停在锁点，
// 事务二随后冲上来——此时它必须在数据库层被挡住，而不是自己数出 0 份。
// 事务一提交后事务二才继续，它会重新数到 1 份从而跳过。
func TestConcurrentGrantsRespectBudgetUnderRowLock(t *testing.T) {
	setup := testDB(t)
	tx := txRollback(t, setup)
	seedCoupon(t, tx, 2001, 24, nil)
	seedRule(t, tx, 1, TriggerFirstRecharge, hours(-1), hours(1), 0,
		withCoupon(2001), withMaxGrants(1))
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("预置数据提交失败：%v", err)
	}
	// 这组数据要提交出去（两个独立事务都得看到同一行规则），
	// 因此用一条独立连接在收尾删掉，不依赖事务回滚。
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

	// outcome 将一笔发券调用的结果与错误传回并发测试控制方；外层事务提交或回滚仍由测试负责。
	type outcome struct {
		// res 当前发券调用返回的 ActivityResult 切片，供比对份数与重放，不证明外层事务已提交。
		res []ActivityResult
		// err 当前调用返回的执行错误；nil 表示该调用成功，不替代持久状态核验。
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

	// 此刻事务一持有 coupon_activity_rule.id=1 的行锁。
	// 事务二从另一条连接冲上来，必须被数据库挡住。
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

	// 给事务二一点时间去撞锁；它应当在锁上阻塞而不是拿到结果。
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

// TestConcurrentGrantsBothSucceedWhenBudgetAllows 钉住锁不会把正常并发也卡死。
//
// 预算足够时，两个用户并发参与都应拿到券。行锁是串行化手段，
// 不是拒绝服务的开关——预算充足时它只该让第二个事务多等一会儿。
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

	// outcome 将一笔发券调用的结果与错误传回并发测试控制方；外层事务提交或回滚仍由测试负责。
	type outcome struct {
		// res 当前发券调用返回的 ActivityResult 切片，供比对份数与重放，不证明外层事务已提交。
		res []ActivityResult
		// err 当前调用返回的执行错误；nil 表示该调用成功，不替代持久状态核验。
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

	// 顺序要紧：先等事务一跑完并提交，锁随之释放，事务二才可能拿到它。
	// 若是先同时等两个结果，事务一迟迟不提交，事务二会一直卡在锁上直到超时。
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
