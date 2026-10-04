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

type resources struct {
	DB           *sql.DB
	ORM          *gorm.DB
	InternalHTTP *http.Client
}

func openResources(ctx context.Context, cfg config.Gateway) (_ *resources, err error) {
	r := &resources{}
	defer func() {
		if err != nil {
			err = errors.Join(err, r.Close())
		}
	}()
	if r.DB, err = dbconn.Open(ctx, cfg.DatabaseURL); err != nil {
		return nil, err
	}
	if r.ORM, err = dbconn.WrapGORM(r.DB); err != nil {
		return nil, err
	}
	r.InternalHTTP = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return r, nil
}
func (r *resources) Close() error {
	if r.InternalHTTP != nil {
		r.InternalHTTP.CloseIdleConnections()
	}
	if r.DB != nil {
		return r.DB.Close()
	}
	return nil
}
