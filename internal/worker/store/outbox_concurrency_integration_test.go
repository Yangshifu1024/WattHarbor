//go:build integration

package store

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"gorm.io/gorm"
)

// 事件外盒领取的真库并发测试。
//
// worker 有三个 publisher（gateway / user / admin），生产上多副本同时扫同一张表。
// `ListPublishableOutbox` 用 `FOR UPDATE SKIP LOCKED`：各副本各取各的，
// 既不互相排队，也不会把同一条事件投递两次。
//
// 这条性质只在真库的行锁语义下存在——SKIP LOCKED 是 MySQL 特有的，
// 换成别的数据库整个假设就变了。单测和内存替身都验不了。

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
	// 全仓 integration 同时写业务 Outbox；领取仅限本测试前缀，避免发布或修改其他包的事件。
	// 仍经过真实 FOR UPDATE SKIP LOCKED、唯一键及退避 SQL，不用替身模拟行锁。
	orm = orm.Scopes(func(q *gorm.DB) *gorm.DB {
		if q.Statement.Table == "event_outbox" { return q.Where("event_id LIKE ?", "OB-%") }
		return q
	}).Session(&gorm.Session{})
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

// armOnOutboxLock 装回调：第一次锁定外盒行后挂起该事务。
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

// seedOutbox 造 n 条事件，回填它们的事件号。
//
// 断言必须针对这 n 条本身而不是「表里一共 n 条」：
// ListPublishableOutbox 领取的是全表所有到期行，
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

func cleanupOutbox(t *testing.T, db *gorm.DB, prefix string) {
	t.Helper()
	if err := db.Table("event_outbox").Where("event_id LIKE ?", prefix+"%").Delete(nil).Error; err != nil {
		t.Errorf("清理外盒事件失败：%v", err)
	}
}

// outboxIDs 把领取结果收成事件号列表，便于直接比对两批是否重叠。
func outboxIDs(rows []ops.EventOutboxRecord) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.EventID)
	}
	return out
}

// TestConcurrentOutboxClaimReturnsDisjointRows 钉住多副本领取互不重叠。
//
// 事务一领取后仍持锁未提交，事务二必须拿到**另一批**事件而不是同一批。
// 拿到同一批就意味着同一条事件被投递两次——消费端虽有 event_id 去重，
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

	type claim struct {
		ids []string
		err error
	}
	done1 := make(chan claim, 1)
	go func() {
		rows, err := ListPublishableOutbox(tx1, "event_outbox")
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
		rows, err := ListPublishableOutbox(tx2, "event_outbox")
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

// TestMarkOutboxPublishedRemovesFromNextClaim 钉住发布成功后不再被领到。
func TestMarkOutboxPublishedRemovesFromNextClaim(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	const prefix = "OB-PUB"

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupOutbox(t, janitor, prefix) })
	cleanupOutbox(t, setup, prefix)
	seedOutbox(t, setup, prefix, 2)

	rows, err := ListPublishableOutbox(setup, "event_outbox")
	if err != nil {
		t.Fatalf("领取失败：%v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应领到 2 条，实际 %d", len(rows))
	}
	for _, r := range rows {
		if err := MarkOutboxPublished(setup, "event_outbox", r.ID); err != nil {
			t.Fatalf("标记已发布失败：%v", err)
		}
	}
	again, err := ListPublishableOutbox(setup, "event_outbox")
	if err != nil {
		t.Fatalf("二次领取失败：%v", err)
	}
	if len(again) != 0 {
		t.Errorf("已发布的事件被再次领到 %d 条——同一事件会被重复投递", len(again))
	}
}

// TestMarkOutboxFailedAppliesBackoff 钉住失败退避真的会推迟重试。
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

	rows, err := ListPublishableOutbox(setup, "event_outbox")
	if err != nil {
		t.Fatalf("领取失败：%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("应领到 1 条，实际 %d", len(rows))
	}
	if err := MarkOutboxFailed(setup, "event_outbox", rows[0].ID, "下游超时", 30); err != nil {
		t.Fatalf("标记失败失败：%v", err)
	}

	again, err := ListPublishableOutbox(setup, "event_outbox")
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
