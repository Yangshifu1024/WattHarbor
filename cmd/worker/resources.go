package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// database 组合一个 SQL 连接池及其 GORM 入口，ORM 不另持连接池。
type database struct {
	// SQL 所属数据库的 SQL 连接池，由外层资源统一关闭。
	SQL *sql.DB
	// ORM 同一 SQL 连接池的 GORM 入口，不拥有独立连接生命周期。
	ORM *gorm.DB
}

// resources 持有该进程打开的资源；受管理请求与任务退出后由 run 关闭，初始化失败由打开方清理。
type resources struct {
	// GatewayDB gateway_db 的 SQL 池及 GORM 入口，用于设备事件与 Outbox 发布。
	GatewayDB database
	// CentralDB central_db 的 SQL 池及 GORM 入口，用于中央业务 Outbox 发布。
	CentralDB database
	// WorkerDB worker_db 的 SQL 池及 GORM 入口，用于调度任务。
	WorkerDB database
	// Stream 事件流 Redis 客户端，资源关闭时位于 HTTP 闲置连接之后、三库之前。
	Stream *redis.Client
	// InternalHTTP 内部服务调用客户端；超时由进程装配固定，关闭时仅清理闲置连接，调用方先等待在途请求。
	InternalHTTP *http.Client
	// JobHTTP 资金任务专用 HTTP 客户端，固定三十秒超时；请求退出后先释放其闲置连接。
	JobHTTP *http.Client
}

// openDatabase 接收初始化 ctx 与数据库连接 URL，打开 SQL 池并包装为同池 GORM 入口。
// 成功返回 database 与 nil，由调用方关闭 SQL 池；打开失败返回零值与原错误，包装失败先关闭已打开池并合并关闭错误。
func openDatabase(ctx context.Context, url string) (database, error) {
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		return database{}, err
	}
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		return database{}, errors.Join(err, db.Close())
	}
	return database{SQL: db, ORM: orm}, nil
}

// openResources 接收初始化 ctx 与 worker 配置 cfg，以实际 openDatabase 委托三库、Redis 和 HTTP 客户端初始化。
// 成功返回由调用方负责关闭的 resources 与 nil；失败返回 nil 与已合并清理错误，不留下此前打开的资源。
func openResources(ctx context.Context, cfg config.Worker) (_ *resources, err error) {
	return openResourcesWithDatabase(ctx, cfg, openDatabase)
}

// openResourcesWithDatabase 接收初始化 ctx、worker 配置 cfg 及数据库打开函数 open；open 必须在成功时转交池所有权，在自身失败时清理其未返回资源。
// 按 gateway、central、worker 顺序打开三库，再检查事件流 Redis 并创建五秒查询与三十秒资金 HTTP 客户端。
// 成功返回由调用方释放的 resources 与 nil；失败返回 nil 与带库身份的打开错误或 Redis 错误，并合并此前资源的关闭错误。
func openResourcesWithDatabase(ctx context.Context, cfg config.Worker, open func(context.Context, string) (database, error)) (_ *resources, err error) {
	r := &resources{}
	defer func() {
		if err != nil {
			err = errors.Join(err, r.Close())
		}
	}()
	if r.GatewayDB, err = open(ctx, cfg.GatewayDatabaseURL); err != nil {
		return nil, fmt.Errorf("gateway database: %w", err)
	}
	if r.CentralDB, err = open(ctx, cfg.CentralDatabaseURL); err != nil {
		return nil, fmt.Errorf("central database: %w", err)
	}
	if r.WorkerDB, err = open(ctx, cfg.DatabaseURL); err != nil {
		return nil, fmt.Errorf("worker database: %w", err)
	}
	options, err := redis.ParseURL(cfg.RedisStreamURL)
	if err != nil {
		return nil, err
	}
	r.Stream = redis.NewClient(options)
	if err = r.Stream.Ping(ctx).Err(); err != nil {
		return nil, err
	}
	r.InternalHTTP = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r.JobHTTP = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return r, nil
}

// Close 无入参，要求 r 的请求与后台任务已全部退出；先清理资金、查询 HTTP 闲置连接，再关闭事件流 Redis，最后按 worker、central、gateway 关闭 SQL 池。
// 返回所有 Redis 与 SQL 关闭错误的合并值，无错误为 nil；跳过未初始化资源，ORM 与 SQL 共用池，不单独关闭。
func (r *resources) Close() error {
	var failures []error
	if r.JobHTTP != nil {
		r.JobHTTP.CloseIdleConnections()
	}
	if r.InternalHTTP != nil {
		r.InternalHTTP.CloseIdleConnections()
	}
	if r.Stream != nil {
		failures = append(failures, r.Stream.Close())
	}
	for _, db := range []*sql.DB{r.WorkerDB.SQL, r.CentralDB.SQL, r.GatewayDB.SQL} {
		if db != nil {
			failures = append(failures, db.Close())
		}
	}
	return errors.Join(failures...)
}
