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

// pass 放行该事务。可重复调用，测试里既有显式放行也有 defer 兜底。
func (g *lockGate) pass() { g.passOne.Do(func() { close(g.release) }) }

// armOnLock 装回调：本次执行里第一次出现「带 FOR UPDATE 且命中 wantTable」的
// 查询时，在它返回之后把事务挂起，等主控放行。
func armOnLock(t *testing.T, db *gorm.DB, gate *lockGate, name, wantTable string) {
	t.Helper()
	err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if !strings.Contains(sql, "FOR UPDATE") || !strings.Contains(sql, wantTable) {
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
