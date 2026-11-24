package worker

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"wattharbor/internal/platform/config"
)

// trackedConnector 提供共享关闭计数的 SQL 测试连接器，不连接外部数据库。
type trackedConnector struct {
	closes *atomic.Int32
}

func (c trackedConnector) Connect(context.Context) (driver.Conn, error) {
	return trackedConnection{c.closes}, nil
}

func (c trackedConnector) Driver() driver.Driver { return trackedDriver{c.closes} }

// trackedDriver 提供共享关闭计数的 SQL 测试驱动，不解析或使用外部连接地址。
type trackedDriver struct {
	closes *atomic.Int32
}

func (d trackedDriver) Open(string) (driver.Conn, error) { return trackedConnection{d.closes}, nil }

// trackedConnection 提供关闭计数与固定拒绝的 SQL 测试连接，不支持语句或事务。
type trackedConnection struct {
	closes *atomic.Int32
}

func (c trackedConnection) Close() error { c.closes.Add(1); return nil }

func (c trackedConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unsupported")
}

func (c trackedConnection) Begin() (driver.Tx, error) { return nil, errors.New("unsupported") }

func (c trackedConnection) Ping(context.Context) error { return nil }

// TestInitializationFailureClosesEveryOpenedDatabase 验证第二、第三数据库或 Redis 初始化失败时，保留错误并关闭此前打开的连接。
// 使用记录关闭次数的测试驱动，不连接真实数据库。
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
