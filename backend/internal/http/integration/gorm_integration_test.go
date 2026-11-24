//go:build integration

package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"
	"wattharbor/internal/platform/dbconn"
)

// testConn 报告 TEST_MYSQL_URL 缺失或建连错误，返回真实 MySQL 的 GORM 句柄及关闭函数。
// 建连受 10 秒超时限制，包装失败立即关闭连接；不创建 schema，调用方负责事务及数据隔离。
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
