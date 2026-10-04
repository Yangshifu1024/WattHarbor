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

type database struct {
	SQL *sql.DB
	ORM *gorm.DB
}
type resources struct {
	GatewayDB    database
	CentralDB    database
	WorkerDB     database
	Stream       *redis.Client
	InternalHTTP *http.Client
	JobHTTP      *http.Client
}

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
func openResources(ctx context.Context, cfg config.Worker) (_ *resources, err error) {
	return openResourcesWithDatabase(ctx, cfg, openDatabase)
}

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
