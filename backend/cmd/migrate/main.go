// 命令 migrate 按 Goose 记录执行各数据库的初始化 SQL，默认处理 gateway、central 和 worker 三个库。
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	"wattharbor/internal/platform/dbconn"
)

var schemas = []string{"gateway_db", "central_db", "worker_db"}

func main() {
	selected := flag.String("schema", "all", "schema name, all, or unified")
	directory := flag.String("dir", "migrations", "migration root directory")
	flag.Parse()
	if err := run(*selected, *directory); err != nil {
		fmt.Fprintln(os.Stderr, "migration failed:", err)
		os.Exit(1)
	}
}

func run(selected, directory string) error {
	if selected == "unified" {
		return migrateUnified(directory)
	}
	if selected != "all" && !contains(schemas, selected) {
		return fmt.Errorf("unknown schema %q", selected)
	}
	if err := goose.SetDialect("mysql"); err != nil {
		return err
	}
	for _, schema := range schemas {
		if selected != "all" && selected != schema {
			continue
		}
		key := "DATABASE_URL_" + strings.ToUpper(strings.TrimSuffix(schema, "_db"))
		raw := os.Getenv(key)
		if raw == "" {
			return fmt.Errorf("%s is required", key)
		}
		dsn, err := dbconn.DSN(raw, false)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		err = migrateOne(ctx, db, schema, filepath.Join(directory, schema))
		cancel()
		_ = db.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", schema, err)
		}
		fmt.Println(schema, "migrations applied")
	}
	return nil
}

// migrateOne 核对目标库名和迁移记录，拒绝无 Goose 记录的非空库和不属于当前迁移集的版本。
func migrateOne(ctx context.Context, db *sql.DB, schema, directory string) error {
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	var actualSchema string
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&actualSchema); err != nil {
		return err
	}
	if actualSchema != schema {
		return fmt.Errorf("database URL targets %q, expected %q", actualSchema, schema)
	}
	var tableCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name <> 'goose_db_version'`).Scan(&tableCount); err != nil {
		return err
	}
	var versionTableCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'goose_db_version'`).Scan(&versionTableCount); err != nil {
		return err
	}
	if tableCount > 0 && versionTableCount == 0 {
		return errors.New("schema already contains tables without Goose history; rebuild test database first")
	}
	if versionTableCount > 0 {
		version, err := goose.GetDBVersionContext(ctx, db)
		if err != nil {
			return err
		}
		migrations, err := goose.CollectMigrations(directory, 0, goose.MaxVersion)
		if err != nil {
			return err
		}
		if version != 0 && !slices.ContainsFunc(migrations, func(m *goose.Migration) bool { return m.Version == version }) {
			return fmt.Errorf("unknown migration version %d", version)
		}
	}
	return goose.UpContext(ctx, db, directory)
}

func contains(items []string, wanted string) bool {
	return slices.Contains(items, wanted)
}
