//go:build integration

package http

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"gorm.io/gorm"
	"os"
	"testing"
	"time"
)

func testConn(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_URL")
	if raw == "" {
		t.Fatal("TEST_MYSQL_URL 未设置，真库测试不能跳过")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := dbconn.Open(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return orm, func() { _ = db.Close() }
}
