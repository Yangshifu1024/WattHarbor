package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"chargepilot/internal/platform/config"
	"chargepilot/internal/platform/dbconn"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// resources 持有该进程打开的资源；受管理请求与任务退出后由 run 关闭，初始化失败由打开方清理。
type resources struct {
	// DB 该进程数据库的 SQL 连接池；生命周期由 resources 管理，受管理任务退出后关闭。
	DB *sql.DB
	// ORM 同一 SQL 连接池的 GORM 入口，不拥有独立连接生命周期。
	ORM *gorm.DB
	// Cache 缓存 Redis 客户端，初始化时检查可达性；resources 在受管理请求退出后关闭。
	Cache *redis.Client
	// InternalHTTP 内部服务调用客户端；超时由进程装配固定，关闭时仅清理闲置连接，调用方先等待在途请求。
	InternalHTTP *http.Client
}

// openResources 接收初始化 ctx 与进程配置 cfg，打开 SQL 池及 GORM 入口，解析并检查缓存 Redis，再创建内部 HTTP 客户端。
// 成功返回 resources 与 nil，调用方负责等待任务退出后关闭；任一步失败返回 nil，并在具名 err 中合并此前资源释放错误，数据库检查响应 ctx。
func openResources(ctx context.Context, cfg config.User) (_ *resources, err error) {
	r := &resources{}
	defer func() {
		if err != nil {
			err = errors.Join(err, r.Close())
		}
	}()
	if r.DB, err = dbconn.Open(ctx, cfg.DatabaseURL, dbconn.PoolConfig{
		MaxOpen: cfg.MaxOpen, MaxIdle: cfg.MaxIdle, Lifetime: cfg.Lifetime, IdleTime: cfg.IdleTime,
	}); err != nil {
		return nil, err
	}
	if r.ORM, err = dbconn.WrapGORM(r.DB); err != nil {
		return nil, err
	}
	options, err := redis.ParseURL(cfg.RedisCacheURL)
	if err != nil {
		return nil, err
	}
	r.Cache = redis.NewClient(options)
	if err = r.Cache.Ping(ctx).Err(); err != nil {
		return nil, err
	}
	r.InternalHTTP = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return r, nil
}

// Close 无入参，要求 r 的受管理请求与后台任务已全部退出；先清理内部 HTTP 闲置连接，再关闭缓存 Redis 与 SQL 池。
// 返回 Redis 与 SQL 关闭错误的合并值，无错误为 nil；跳过未初始化资源，ORM 不单独关闭，本方法不等待在途任务。
func (r *resources) Close() error {
	var failures []error
	if r.InternalHTTP != nil {
		r.InternalHTTP.CloseIdleConnections()
	}
	if r.Cache != nil {
		failures = append(failures, r.Cache.Close())
	}
	if r.DB != nil {
		failures = append(failures, r.DB.Close())
	}
	return errors.Join(failures...)
}
