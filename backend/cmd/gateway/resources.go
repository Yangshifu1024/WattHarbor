package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"gorm.io/gorm"
)

// resources 持有该进程打开的资源；受管理请求与任务退出后由 run 关闭，初始化失败由打开方清理。
type resources struct {
	// DB 该进程数据库的 SQL 连接池；生命周期由 resources 管理，受管理任务退出后关闭。
	DB *sql.DB
	// ORM 同一 SQL 连接池的 GORM 入口，不拥有独立连接生命周期。
	ORM *gorm.DB
	// InternalHTTP 内部服务调用客户端；超时由进程装配固定，关闭时仅清理闲置连接，调用方先等待在途请求。
	InternalHTTP *http.Client
}

// openResources 接收初始化 ctx 与进程配置 cfg，打开 SQL 池及 GORM 入口，创建内部 HTTP 客户端。
// 成功返回 resources 与 nil，调用方负责等待任务退出后关闭；任一步失败返回 nil，并在具名 err 中合并此前资源释放错误，数据库检查响应 ctx。
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

// Close 无入参，要求 r 的 HTTP 请求、设备处理器与重试任务已全部退出；先清理内部 HTTP 闲置连接，再关闭 SQL 池。
// 返回 SQL 关闭错误，未打开 SQL 池时返回 nil；ORM 不单独关闭，本方法不等待在途任务。
func (r *resources) Close() error {
	if r.InternalHTTP != nil {
		r.InternalHTTP.CloseIdleConnections()
	}
	if r.DB != nil {
		return r.DB.Close()
	}
	return nil
}
