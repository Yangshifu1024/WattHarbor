package dbconn

import (
	"context"
	"github.com/go-sql-driver/mysql"
	"strings"
	"testing"
	"time"
)

func TestDSN(t *testing.T) {
	dsn, err := DSN("mysql://wattharbor:p%40ss@localhost:3306/user_db", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "p@ss") || !strings.Contains(dsn, "user_db") {
		t.Fatal(dsn)
	}
	for _, bad := range []string{"", "postgres://a:b@host/db", "mysql://a:b@host", "mysql://a:b@host/db/other"} {
		if _, err := DSN(bad, false); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestDSNSessionSettings(t *testing.T) {
	dsn, err := DSN("mysql://fixture:fixture@localhost/fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loc != time.UTC || cfg.Params["time_zone"] != "'+00:00'" || !strings.Contains(cfg.Params["sql_mode"], "STRICT_TRANS_TABLES") || !strings.Contains(cfg.Params["sql_mode"], "NO_ZERO_DATE") {
		t.Fatalf("missing UTC or strict session settings: %+v", cfg.Params)
	}
}

func TestOpenRejectsPoolBeforeConnecting(t *testing.T) {
	for _, pool := range []PoolConfig{{MaxOpen: -1}, {MaxIdle: -1}, {Lifetime: -time.Second}, {IdleTime: -time.Second}, {MaxOpen: 2, MaxIdle: 3}} {
		if _, err := Open(context.Background(), "mysql://fixture:fixture@localhost/fixture", pool); err == nil || !strings.Contains(err.Error(), "pool") {
			t.Fatalf("invalid pool accepted: %v", err)
		}
	}
}
