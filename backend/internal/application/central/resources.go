package central

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"wattharbor/internal/platform/config"
	"wattharbor/internal/platform/dbconn"
)

// resources 持有模块的连接，初始化失败或应用完全退出后统一关闭。
type resources struct {
	// DB 是模块的 SQL 连接池，待请求和任务退出后关闭。
	DB *sql.DB
	// ORM 与 SQL 共用连接池，无需单独关闭。
	ORM *gorm.DB
	// Cache 是缓存 Redis 客户端，启动时检查连接，退出时关闭。
	Cache *redis.Client
	// InternalHTTP 由单体入口注入并复用。
	InternalHTTP *http.Client
}

// openResources 打开本进程的数据库和 Redis；失败时释放已打开的资源。
func openResources(ctx context.Context, cfg config.Central) (_ *resources, err error) {
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
	return r, nil
}

// Close 依次关闭 Redis 和 SQL 池，返回合并后的关闭错误。
// 调用前必须等待请求和任务退出。
func (r *resources) Close() error {
	var failures []error
	if r.Cache != nil {
		failures = append(failures, r.Cache.Close())
	}
	if r.DB != nil {
		failures = append(failures, r.DB.Close())
	}
	return errors.Join(failures...)
}
