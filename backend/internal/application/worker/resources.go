package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"wattharbor/internal/platform/config"
	"wattharbor/internal/platform/dbconn"
)

// database 组合一个 SQL 连接池及其 GORM 入口，ORM 不另持连接池。
type database struct {
	// SQL 所属数据库的 SQL 连接池，由外层资源统一关闭。
	SQL *sql.DB
	// ORM 与 SQL 共用连接池，无需单独关闭。
	ORM *gorm.DB
}

// resources 持有模块的连接，初始化失败或应用完全退出后统一关闭。
type resources struct {
	// GatewayDB gateway_db 的 SQL 池及 GORM 入口，用于设备事件与 Outbox 发布。
	GatewayDB database
	// CentralDB central_db 的 SQL 池及 GORM 入口，用于中央业务 Outbox 发布。
	CentralDB database
	// WorkerDB worker_db 的 SQL 池及 GORM 入口，用于调度任务。
	WorkerDB database
	// Stream 是事件流 Redis 客户端，在数据库之前关闭。
	Stream *redis.Client
	// InternalHTTP 由单体入口注入并复用。
	InternalHTTP *http.Client
}

// openDatabase 打开 SQL 连接池并创建共用该池的 GORM 入口；包装失败时关闭连接池。
func openDatabase(ctx context.Context, url string, pools ...dbconn.PoolConfig) (database, error) {
	db, err := dbconn.Open(ctx, url, pools...)
	if err != nil {
		return database{}, err
	}
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		return database{}, errors.Join(err, db.Close())
	}
	return database{SQL: db, ORM: orm}, nil
}

// openResources 初始化三库和事件流 Redis；失败时释放已打开的资源。
func openResources(ctx context.Context, cfg config.Worker) (_ *resources, err error) {
	pool := dbconn.PoolConfig{MaxOpen: cfg.MaxOpen, MaxIdle: cfg.MaxIdle, Lifetime: cfg.Lifetime, IdleTime: cfg.IdleTime}
	return openResourcesWithDatabase(ctx, cfg, func(ctx context.Context, url string) (database, error) {
		return openDatabase(ctx, url, pool)
	})
}

// openResourcesWithDatabase 依次打开 gateway、central、worker 数据库，再检查 Redis；失败时关闭已打开的资源。
// open 成功时交出连接池，失败时自行清理未返回的资源。
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
	return r, nil
}

// Close 先关闭 Redis，再依次关闭 worker、central、gateway 数据库。
// 调用前必须等待请求和任务退出；返回合并后的关闭错误。
func (r *resources) Close() error {
	var failures []error
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
