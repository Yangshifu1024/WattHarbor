package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// resources 持有后台进程打开的连接，所有受管理请求及清理循环退出后按 Close 释放。
type resources struct {
	DB           *sql.DB       // central SQL 连接池，最后释放
	ORM          *gorm.DB      // 同一 SQL 池的 GORM 入口，无独立连接生命周期
	Cache        *redis.Client // 后台会话及缓存 Redis 连接
	InternalHTTP *http.Client  // 有界内部调用连接池，受管理请求退出后释放闲置连接
}

// openResources 在 ctx 下按 cfg 初始化数据库、缓存及内部 HTTP 池，返回资源或初始化错误。
// 任一步失败会释放此前打开的资源，不留下启动失败后的连接。
// cfg 提供数据库与缓存 URL；ctx 传入连接可达性检查，成功后所有权交给返回的资源，失败时返回 nil 与合并清理错误。
func openResources(ctx context.Context, cfg config.Admin) (_ *resources, err error) {
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
	r.InternalHTTP = &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), Timeout: 10 * time.Second}

	return r, nil
}

// Close 关闭 r 内部 HTTP 闲置连接、Redis 及 SQL 池，返回所有释放错误；调用前须等待请求和循环退出。
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
