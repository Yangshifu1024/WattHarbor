//go:build integration

package repo

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/dbconn"
)

// 事件外盒领取的真库并发测试。
//
// worker 有三个 publisher（gateway / user / admin），生产上多副本同时扫同一张表。
// `ListPublishableOutbox` 用 `FOR UPDATE SKIP LOCKED`：各副本在本次并发领取中各取未锁行。
// Redis 已收到而 MySQL 回滚仍可导致后续重复发布，本测试不替代调用方幂等验收。
//
// 本项目以真实 MySQL 的行锁验证该性质，单测和内存替身不作为锁证据。

const testMySQLURLEnv = "TEST_MYSQL_URL"

// testConn 为 t 打开独立真库连接并返回关闭函数；缺配置或失败终止，OB 前缀 Scope 隔离造数。
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
	// 全仓 integration 同时写业务 Outbox；领取仅限本测试前缀，避免发布或修改其他包的事件。
	// 仍经过真实 FOR UPDATE SKIP LOCKED、唯一键及退避 SQL，不用替身模拟行锁。
	orm = orm.Scopes(func(q *gorm.DB) *gorm.DB {
		if q.Statement.Table == "event_outbox" {
			return q.Where("event_id LIKE ?", "OB-%")
		}
		return q
	}).Session(&gorm.Session{})
	return orm, func() { _ = sqlDB.Close() }
}

// lockGate 把一个事务卡在某条带 FOR UPDATE 的语句返回之后。
type lockGate struct {
	locked  chan struct{} // 首次实际行锁已取得的闸门信号。
	release chan struct{} // 允许持锁查询返回，由 pass 关闭。
	once    sync.Once     // 仅首个匹配查询进入持锁闸门。
	passOne sync.Once     // 防止重复放行关闭 channel。
}

// newLockGate 返回未放行的行锁闸门，无数据库或后台任务。
func newLockGate() *lockGate {
	return &lockGate{locked: make(chan struct{}), release: make(chan struct{})}
}

// await 等待 g 的实际行锁信号，超过 15 秒由 t 报告 what 对应的失败，不以 sleep 判定并发次序。
func (g *lockGate) await(t *testing.T, what string) {
	t.Helper()
	select {
	case <-g.locked:
	case <-time.After(15 * time.Second):
		t.Fatalf("%s 迟迟没有到达加锁语句", what)
	}
}

// pass 放行 g 的持锁查询，重复调用无副作用，不提交或释放数据库事务。
func (g *lockGate) pass() { g.passOne.Do(func() { close(g.release) }) }

// armOnOutboxLock 在 db 按 name 注册查询回调，首次真实外盒行锁后通过 gate 挂起，由 t 报告注册错误。
// 只为测试安排顺序，不模拟行锁、提交事务或改变查询结果。
func armOnOutboxLock(t *testing.T, db *gorm.DB, gate *lockGate, name string) {
	t.Helper()
	err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "FOR UPDATE") && strings.Contains(tx.Statement.SQL.String(), "FROM `event_outbox`") {
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

const outboxStream = "charge_ended_stream"

// seedOutbox 在 db 写入 prefix 对应的 n 条到期事件并返回事件号，写入失败终止；调用方清理。
//
// 断言必须针对这 n 条本身而不是「表里一共 n 条」：
// ListPublishableOutbox 领取的是传入连接 Scope 内所有到期行，
// 而 `go test ./...` 会并发跑别的包，那些包的集成测试也会往同一张
// event_outbox 写（钱包到账、退款成功都发事件）。计数写死必然被邻居干扰。
func seedOutbox(t *testing.T, db *gorm.DB, prefix string, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		row := map[string]any{
			"event_id": prefix + string(rune('0'+i)), "stream": outboxStream,
			"envelope_json": []byte(`{"event_id":"` + prefix + string(rune('0'+i)) + `"}`),
			"status":        "pending", "retry_count": 0, "scheduled_at": time.Now().Add(-time.Minute).UTC(),
		}
		if err := db.Table("event_outbox").Create(row).Error; err != nil {
			t.Fatalf("造外盒事件 %d 失败：%v", i, err)
		}
		ids = append(ids, row["event_id"].(string))
	}
	return ids
}

// cleanupOutbox 仅删除 db 中 prefix 对应测试事件，错误由 t 报告，不清理其他前缀或业务资金。
func cleanupOutbox(t *testing.T, db *gorm.DB, prefix string) {
	t.Helper()
	if err := db.Table("event_outbox").Where("event_id LIKE ?", prefix+"%").Delete(nil).Error; err != nil {
		t.Errorf("清理外盒事件失败：%v", err)
	}
}

// outboxIDs 把 rows 的事件号复制为列表，返回独立 slice，无 IO 或状态修改。
func outboxIDs(rows []ops.EventOutboxRecord) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.EventID)
	}
	return out
}

// TestConcurrentOutboxClaimReturnsDisjointRows 用两个真实连接验证行锁多副本本次领取互不重叠。
//
// 事务一领取后仍持锁未提交，事务二必须拿到**另一批**事件而不是同一批。
// 拿到同一批就意味着同一条事件被投递两次——消息处理方虽有 event_id 去重，
// 但那是在故障兜底路径上，不该成为常态。
//
// 若把 SKIP LOCKED 去掉，事务二会阻塞在等锁上，"不阻塞"这条断言立刻失败。
func TestConcurrentOutboxClaimReturnsDisjointRows(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	const prefix = "OB-DISJ"

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupOutbox(t, janitor, prefix) })
	cleanupOutbox(t, setup, prefix)
	seeded := seedOutbox(t, setup, prefix, 4)

	p1, close1 := testConn(t)
	defer close1()
	p2, close2 := testConn(t)
	defer close2()

	gate := newLockGate()
	armOnOutboxLock(t, p1, gate, "test:outbox_lock")
	defer gate.pass()

	tx1 := p1.Begin()
	if tx1.Error != nil {
		t.Fatalf("事务一开启失败：%v", tx1.Error)
	}
	defer tx1.Rollback()

	// claim 传递仍在调用方事务中的 Outbox 锁读身份与错误，用于比对 SKIP LOCKED 结果；返回编号不表示事务提交或事件已发布。
	type claim struct {
		ids []string // 本事务实际领取的事件标识。
		err error    // 仓储查询或锁错误。
	}
	done1 := make(chan claim, 1)
	go func() {
		rows, err := ListPublishableOutbox(tx1, "gateway")
		done1 <- claim{ids: outboxIDs(rows), err: err}
	}()
	gate.await(t, "事务一领取")

	tx2 := p2.Begin()
	if tx2.Error != nil {
		t.Fatalf("事务二开启失败：%v", tx2.Error)
	}
	defer tx2.Rollback()

	done2 := make(chan claim, 1)
	go func() {
		rows, err := ListPublishableOutbox(tx2, "gateway")
		done2 <- claim{ids: outboxIDs(rows), err: err}
	}()

	// SKIP LOCKED 的关键：事务二不排队，直接拿到剩下的。
	var second claim
	select {
	case got := <-done2:
		if got.err != nil {
			t.Fatalf("事务二领取失败：%v", got.err)
		}
		second = got
		if len(got.ids) != 0 {
			t.Errorf("事务一已锁住全部 4 条时，事务二却领到了 %v——两批有重叠，同一事件会被投递两次", got.ids)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("事务二在事务一持锁时阻塞了——SKIP LOCKED 失效，多副本会排队等同一批")
	}

	gate.pass()
	first := <-done1
	if first.err != nil {
		t.Fatalf("事务一领取失败：%v", first.err)
	}
	claimed := map[string]bool{}
	for _, id := range first.ids {
		claimed[id] = true
	}
	for _, id := range seeded {
		if !claimed[id] {
			t.Errorf("事务一没有领到自己造的 %s", id)
		}
	}
	// 两批的并集应等于全部 4 条，且没有任何一条同时出现在两批里。
	union := map[string]bool{}
	for _, id := range first.ids {
		union[id] = true
	}
	for _, id := range second.ids {
		if union[id] {
			t.Errorf("事件 %s 同时被两个副本领到", id)
		}
		union[id] = true
	}
	for _, id := range seeded {
		if !union[id] {
			t.Errorf("两批并集里缺 %s", id)
		}
	}
}

// TestMarkOutboxPublishedRemovesFromNextClaim 用真实 MySQL 验证已标记 published 的事件不再领取。
// 只验证队列 SQL，不连接 Redis；调用方按测试前缀清理记录。
func TestMarkOutboxPublishedRemovesFromNextClaim(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	const prefix = "OB-PUB"

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupOutbox(t, janitor, prefix) })
	cleanupOutbox(t, setup, prefix)
	seedOutbox(t, setup, prefix, 2)

	rows, err := ListPublishableOutbox(setup, "gateway")
	if err != nil {
		t.Fatalf("领取失败：%v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应领到 2 条，实际 %d", len(rows))
	}
	for _, r := range rows {
		if err := MarkOutboxPublished(setup, "gateway", r.ID); err != nil {
			t.Fatalf("标记已发布失败：%v", err)
		}
	}
	again, err := ListPublishableOutbox(setup, "gateway")
	if err != nil {
		t.Fatalf("二次领取失败：%v", err)
	}
	if len(again) != 0 {
		t.Errorf("已发布的事件被再次领到 %d 条——同一事件会被重复投递", len(again))
	}
}

// TestMarkOutboxFailedAppliesBackoff 用真实 MySQL 验证失败计数、错误及 30 秒延后不立即重领。
//
// 退避写成 `DATE_ADD(NOW(3), INTERVAL ? SECOND)`，若间隔算错（比如退避秒数
// 传成 0），坏事件会被立刻重领，10 秒一轮的发布器变成对下游的死循环轰炸。
func TestMarkOutboxFailedAppliesBackoff(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	const prefix = "OB-FAIL"

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupOutbox(t, janitor, prefix) })
	cleanupOutbox(t, setup, prefix)
	seedOutbox(t, setup, prefix, 1)

	rows, err := ListPublishableOutbox(setup, "gateway")
	if err != nil {
		t.Fatalf("领取失败：%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("应领到 1 条，实际 %d", len(rows))
	}
	if err := MarkOutboxFailed(setup, "gateway", rows[0].ID, "下游超时", 30); err != nil {
		t.Fatalf("标记失败失败：%v", err)
	}

	again, err := ListPublishableOutbox(setup, "gateway")
	if err != nil {
		t.Fatalf("二次领取失败：%v", err)
	}
	if len(again) != 0 {
		t.Errorf("退避期内的事件被立刻重领 %d 条——坏事件会反复轰炸下游", len(again))
	}

	var status string
	var lastErr *string
	var retryCount uint
	if err := setup.Table("event_outbox").Where("id = ?", rows[0].ID).
		Select("status, last_error, retry_count").Row().Scan(&status, &lastErr, &retryCount); err != nil {
		t.Fatalf("读外盒事件失败：%v", err)
	}
	if status != "failed" {
		t.Errorf("状态 = %q，期望 failed", status)
	}
	if lastErr == nil || *lastErr != "下游超时" {
		t.Errorf("失败原因 = %v，期望记下「下游超时」", lastErr)
	}
	if retryCount != 1 {
		t.Errorf("retry_count = %d，期望 1", retryCount)
	}
}
