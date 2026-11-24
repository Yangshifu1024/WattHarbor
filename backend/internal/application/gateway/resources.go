package gateway

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"gorm.io/gorm"
	"wattharbor/internal/platform/config"
	"wattharbor/internal/platform/dbconn"
)

// resources 持有模块的连接，初始化失败或应用完全退出后统一关闭。
type resources struct {
	Notify func()
	// DB 是模块的 SQL 连接池，待请求和任务退出后关闭。
	DB *sql.DB
	// ORM 与 SQL 共用连接池，无需单独关闭。
	ORM *gorm.DB
	// InternalHTTP 由单体入口注入并复用。
	InternalHTTP *http.Client
}

// openResources 打开数据库；失败时释放已打开的资源。
func openResources(ctx context.Context, cfg config.Gateway) (_ *resources, err error) {
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
	return r, nil
}

// Close 关闭 SQL 池；调用前必须等待 HTTP、设备和重试任务退出。
func (r *resources) Close() error {
	if r.DB != nil {
		return r.DB.Close()
	}
	return nil
}
