package gateway

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"gorm.io/gorm"
	"wattharbor/internal/platform/config"
	"wattharbor/internal/platform/dbconn"
)

// resources 持有本进程的连接，初始化失败或应用完全退出后统一关闭。
type resources struct {
	Notify func()
	// DB 是本进程的 SQL 连接池，待请求和任务退出后关闭。
	DB *sql.DB
	// ORM 与 SQL 共用连接池，无需单独关闭。
	ORM *gorm.DB
	// InternalHTTP 是内部服务客户端；等待请求结束后清理空闲连接。
	InternalHTTP *http.Client
}

// openResources 打开数据库和内部 HTTP 客户端；失败时释放已打开的资源。
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
	r.InternalHTTP = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return r, nil
}

// Close 清理 HTTP 空闲连接并关闭 SQL 池；调用前必须等待 HTTP、设备和重试任务退出。
func (r *resources) Close() error {
	if r.InternalHTTP != nil {
		r.InternalHTTP.CloseIdleConnections()
	}
	if r.DB != nil {
		return r.DB.Close()
	}
	return nil
}
