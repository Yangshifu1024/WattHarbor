// Package dbconn 校验 MySQL 连接配置并创建 SQL 连接池和 GORM 实例。
package dbconn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DSN 把部署用的 URL 转换成 database/sql 的 MySQL DSN。
// 仅接受带用户名、主机和单个数据库名称的 mysql URL；不建立连接。
func DSN(raw string, multiStatements bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "mysql" || u.Host == "" || u.User == nil || u.User.Username() == "" {
		return "", errors.New("invalid MySQL URL")
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" || strings.Contains(name, "/") {
		return "", errors.New("MySQL URL must name one schema")
	}
	port := u.Port()
	if port == "" {
		port = "3306"
	}
	password, _ := u.User.Password()
	config := mysql.NewConfig()
	config.User = u.User.Username()
	config.Passwd = password
	config.Net = "tcp"
	config.Addr = net.JoinHostPort(u.Hostname(), port)
	config.DBName = name
	config.ParseTime = true
	config.Loc = time.UTC
	config.Timeout = 5 * time.Second
	config.ReadTimeout = 10 * time.Second
	config.WriteTimeout = 10 * time.Second
	config.MultiStatements = multiStatements
	config.Params = map[string]string{
		"charset":   "utf8mb4",
		"time_zone": "'+00:00'",
		"sql_mode":  "'STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION,ERROR_FOR_DIVISION_BY_ZERO,NO_ZERO_DATE,NO_ZERO_IN_DATE'",
	}
	return config.FormatDSN(), nil
}

// PoolConfig 设置连接数上限、连接最长使用时间和最长空闲时间。
type PoolConfig struct {
	MaxOpen  int
	MaxIdle  int
	Lifetime time.Duration
	IdleTime time.Duration
}

// Validate 拒绝负数，以及空闲连接上限大于总连接上限的配置。
func (c PoolConfig) Validate() error {
	if c.MaxOpen < 0 || c.MaxIdle < 0 || c.Lifetime < 0 || c.IdleTime < 0 {
		return errors.New("database pool limits must not be negative")
	}
	if c.MaxIdle > c.MaxOpen {
		return errors.New("database pool idle connections must not exceed open connections")
	}
	return nil
}

// Open 校验 URL 和连接池配置，使用 UTC 时区及严格 SQL 模式，并检查数据库连接。
// 默认最多 20 个连接、5 个空闲连接；连接最多使用 30 分钟、空闲 5 分钟。
func Open(ctx context.Context, raw string, pools ...PoolConfig) (*sql.DB, error) {
	pool := PoolConfig{MaxOpen: 20, MaxIdle: 5, Lifetime: 30 * time.Minute, IdleTime: 5 * time.Minute}
	if len(pools) > 1 {
		return nil, errors.New("only one database pool configuration is allowed")
	}
	if len(pools) == 1 {
		pool = pools[0]
	}
	if err := pool.Validate(); err != nil {
		return nil, err
	}
	dsn, err := DSN(raw, false)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(pool.MaxOpen)
	db.SetMaxIdleConns(pool.MaxIdle)
	db.SetConnMaxLifetime(pool.Lifetime)
	db.SetConnMaxIdleTime(pool.IdleTime)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("MySQL ping: %w", err)
	}
	return db, nil
}

// WrapGORM 使用已有 SQL 连接池创建 GORM 实例，不再连接数据库或自动创建外键。
func WrapGORM(db *sql.DB) (*gorm.DB, error) {
	if db == nil {
		return nil, errors.New("nil MySQL pool")
	}
	return gorm.Open(gormmysql.New(gormmysql.Config{
		Conn:                      db,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DisableAutomaticPing:                     true,
		DisableForeignKeyConstraintWhenMigrating: true,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
}
