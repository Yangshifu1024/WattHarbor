package user

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

// resources 持有本进程的连接，初始化失败或应用完全退出后统一关闭。
type resources struct {
	Notify func()
	// DB 是本进程的 SQL 连接池，待请求和任务退出后关闭。
	DB *sql.DB
	// ORM 与 SQL 共用连接池，无需单独关闭。
	ORM *gorm.DB
	// Cache 是缓存 Redis 客户端，启动时检查连接，退出时关闭。
	Cache *redis.Client
	// InternalHTTP 是内部服务客户端；等待请求结束后清理空闲连接。
	InternalHTTP *http.Client
}

// openResources 打开本进程的数据库、Redis 和 HTTP 客户端；失败时释放已打开的资源。
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

// Close 依次清理 HTTP 空闲连接、Redis 和 SQL 池，返回合并后的关闭错误。
// 调用前必须等待请求退出。
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
