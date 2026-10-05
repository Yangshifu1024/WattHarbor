//go:build integration

package export

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
)

// TestSourceQueryCancellation 使用真实连接验证源取数收到请求取消，不访问开发库或写入数据。
func TestSourceQueryCancellation(t *testing.T) {
	url := os.Getenv("TEST_MYSQL_URL")
	if url == "" {
		t.Skip("TEST_MYSQL_URL is required")
	}
	pool, err := dbconn.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db, err := dbconn.WrapGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	repo := ExportRepo{DB: db}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, resource := range []string{"orders", "stations", "devices", "bills"} {
		_, err := repo.QueryExportSource(ctx, resource, operations.DataScope{Unrestricted: true}, map[string]any{"from": "2026-09-01", "to": "2026-09-02"}, 1)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s ignored cancellation: %v", resource, err)
		}
	}
}
