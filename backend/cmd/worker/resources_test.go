package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
)

// trackedConnector 提供共享关闭计数的 SQL 测试连接器，不连接外部数据库。
type trackedConnector struct {
	// closes 测试共享的原子关闭计数，不代表外部数据库连接。
	closes *atomic.Int32
}

// Connect 忽略传入的 context，返回共享 c.closes 计数器的新测试连接与 nil，不建立外部连接。
func (c trackedConnector) Connect(context.Context) (driver.Conn, error) {
	return trackedConnection{c.closes}, nil
}

// Driver 无入参，返回共享 c.closes 计数器的测试驱动，无连接副作用。
func (c trackedConnector) Driver() driver.Driver { return trackedDriver{c.closes} }

// trackedDriver 提供共享关闭计数的 SQL 测试驱动，不解析或使用外部连接地址。
type trackedDriver struct {
	// closes 测试共享的原子关闭计数，不代表外部数据库连接。
	closes *atomic.Int32
}

// Open 忽略传入的连接字符串，返回共享 d.closes 计数器的新测试连接与 nil，不建立外部连接。
func (d trackedDriver) Open(string) (driver.Conn, error) { return trackedConnection{d.closes}, nil }

// trackedConnection 提供关闭计数与固定拒绝的 SQL 测试连接，不支持语句或事务。
type trackedConnection struct {
	// closes 测试共享的原子关闭计数，不代表外部数据库连接。
	closes *atomic.Int32
}

// Close 无入参，将共享 c.closes 增加一次并返回 nil；用于观测 SQL 池关闭，不释放外部资源。
func (c trackedConnection) Close() error { c.closes.Add(1); return nil }

// Prepare 忽略传入的 SQL 文本，返回 nil 语句与固定 unsupported 错误，不解析或执行 SQL。
func (c trackedConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unsupported")
}

// Begin 无入参，返回 nil 事务与固定 unsupported 错误，不创建事务。
func (c trackedConnection) Begin() (driver.Tx, error) { return nil, errors.New("unsupported") }

// Ping 忽略传入的 context，返回 nil 以满足连接初始化检查，不访问真库。
func (c trackedConnection) Ping(context.Context) error { return nil }

// TestInitializationFailureClosesEveryOpenedDatabase 使用 t 在第二、第三数据库打开或 Redis URL 解析时制造失败，核对原错误、三库打开顺序及此前连接全部关闭；使用计数驱动，不访问真库。
func TestInitializationFailureClosesEveryOpenedDatabase(t *testing.T) {
	for _, failAt := range []int{2, 3, 4} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			var calls []string
			var pools []*sql.DB
			var closes atomic.Int32
			failure := errors.New("database initialization failed")
			open := func(ctx context.Context, url string) (database, error) {
				calls = append(calls, url)
				if len(calls) == failAt {
					return database{}, failure
				}
				pool := sql.OpenDB(trackedConnector{&closes})
				pools = append(pools, pool)
				t.Cleanup(func() { _ = pool.Close() })
				if err := pool.PingContext(ctx); err != nil {
					t.Fatal(err)
				}
				return database{SQL: pool}, nil
			}
			cfg := config.Worker{GatewayDatabaseURL: "gateway", CentralDatabaseURL: "central", DatabaseURL: "worker", RedisStreamURL: "invalid-redis-url"}
			r, err := openResourcesWithDatabase(context.Background(), cfg, open)
			if err == nil || r != nil {
				t.Fatalf("want initialization failure, got %v %v", r, err)
			}
			if failAt < 4 && !errors.Is(err, failure) {
				t.Fatalf("original error lost: %v", err)
			}
			if !reflect.DeepEqual(calls, []string{"gateway", "central", "worker"}[:min(failAt, 3)]) {
				t.Fatalf("database identity/order: %v", calls)
			}
			if got := closes.Load(); got != int32(len(pools)) {
				t.Fatalf("closed %d of %d connections", got, len(pools))
			}
			for _, pool := range pools {
				if err := pool.Ping(); err == nil {
					t.Fatal("failed initialization left pool open")
				}
			}
		})
	}
}
