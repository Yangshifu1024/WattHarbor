//go:build integration

package dbconn

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMySQLSessionsUseUTCAndStrictMode 在两个物理连接上验证 DSN 对每个新会话应用 UTC 与严格模式。
func TestMySQLSessionsUseUTCAndStrictMode(t *testing.T) {
	raw := os.Getenv("TEST_MYSQL_URL")
	if raw == "" {
		t.Fatal("TEST_MYSQL_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := Open(ctx, raw, PoolConfig{MaxOpen: 2, MaxIdle: 2, Lifetime: time.Minute, IdleTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, conn := range []*sql.Conn{first, second} {
		var zone, mode string
		var offset int
		if err := conn.QueryRowContext(ctx, "SELECT @@session.time_zone, @@session.sql_mode, TIMESTAMPDIFF(SECOND, UTC_TIMESTAMP(), NOW())").Scan(&zone, &mode, &offset); err != nil {
			t.Fatal(err)
		}
		if zone != "+00:00" || offset != 0 || !strings.Contains(mode, "STRICT_TRANS_TABLES") || !strings.Contains(mode, "NO_ZERO_DATE") {
			t.Fatalf("session zone=%s mode=%s offset=%d", zone, mode, offset)
		}
	}
}
