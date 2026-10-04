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

type trackedConnector struct{ closes *atomic.Int32 }

func (c trackedConnector) Connect(context.Context) (driver.Conn, error) {
	return trackedConnection{c.closes}, nil
}
func (c trackedConnector) Driver() driver.Driver { return trackedDriver{c.closes} }

type trackedDriver struct{ closes *atomic.Int32 }

func (d trackedDriver) Open(string) (driver.Conn, error) { return trackedConnection{d.closes}, nil }

type trackedConnection struct{ closes *atomic.Int32 }

func (c trackedConnection) Close() error { c.closes.Add(1); return nil }
func (c trackedConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unsupported")
}
func (c trackedConnection) Begin() (driver.Tx, error)  { return nil, errors.New("unsupported") }
func (c trackedConnection) Ping(context.Context) error { return nil }

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
