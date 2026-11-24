//go:build integration

package wallet

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"
	"wattharbor/internal/platform/dbconn"
)

// walletDB 为当前测试打开独立真库连接池；缺少配置即失败，清理只关闭自身池。
func walletDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_URL")
	if raw == "" {
		t.Fatal("TEST_MYSQL_URL is required")
	}
	pool, err := dbconn.Open(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	db, err := dbconn.WrapGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// walletTransaction 提供有界的夹具事务；测试结束严格回滚，再取消事务 context。
func walletTransaction(t *testing.T, db *gorm.DB) *gorm.DB {
	t.Helper()
	// testing 在 Cleanup 前取消 t.Context，database/sql 会抢先回滚该 context
	// 的事务。夹具自己负责有界 context，并按回滚→取消顺序释放，避免两条回滚路径竞态。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		cancel()
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() {
		defer cancel()
		if err := tx.Rollback().Error; err != nil {
			t.Error(err)
		}
	})
	return tx
}
