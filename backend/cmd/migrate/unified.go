package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"chargepilot/internal/platform/dbconn"
	"github.com/pressly/goose/v3"
)

const unifiedSchema = "chargepilot"

// unifiedMigrations 固定各原始迁移在单库中的版本；新版本只能在末尾追加。
var unifiedMigrations = []string{
	"central_db/0001_init.sql",
	"gateway_db/0001_init.sql",
	"worker_db/0001_init.sql",
	"central_db/0002_invoice_active_unique.sql",
}

func migrateUnified(sources string) (err error) {
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		return errors.New("DATABASE_URL is required")
	}
	dsn, err := dbconn.DSN(raw, false)
	if err != nil {
		return err
	}
	assembled, err := os.MkdirTemp("", "chargepilot-unified-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(assembled)) }()
	if err = assembleUnified(sources, assembled); err != nil {
		return err
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	if err = goose.SetDialect("mysql"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return migrateOne(ctx, db, unifiedSchema, assembled)
}

func assembleUnified(sources, assembled string) error {
	for i, source := range unifiedMigrations {
		contents, err := os.ReadFile(filepath.Join(sources, source))
		if err != nil {
			return err
		}
		if source == "gateway_db/0001_init.sql" {
			name := []byte("`event_outbox`")
			if bytes.Count(contents, []byte("CREATE TABLE `event_outbox`")) != 1 {
				return errors.New("gateway outbox migration no longer matches")
			}
			contents = bytes.ReplaceAll(contents, name, []byte("`gateway_event_outbox`"))
		}
		dest := filepath.Join(assembled, fmt.Sprintf("%04d_%s.sql", i+1, filepath.Base(filepath.Dir(source))))
		if err = os.WriteFile(dest, contents, 0600); err != nil {
			return err
		}
	}
	return nil
}
