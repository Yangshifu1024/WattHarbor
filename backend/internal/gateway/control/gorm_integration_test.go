//go:build integration

package control

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"
)

// awaitGatewayDeviceLock 用真实锁等待记录确认第二条独立连接被设备行锁挡住。
// 轮询只限制观测频率；无权限、提前返回或超时均失败，不依赖起协程的时间碰运气。
func awaitGatewayDeviceLock(t *testing.T, ctx context.Context, observer *gorm.DB, waiter, blocker uint64, done <-chan error) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int64
		err := observer.WithContext(ctx).Raw(`SELECT COUNT(*) FROM performance_schema.data_lock_waits w
JOIN performance_schema.data_locks l ON l.ENGINE=w.ENGINE AND l.ENGINE_LOCK_ID=w.REQUESTING_ENGINE_LOCK_ID
JOIN performance_schema.threads r ON r.THREAD_ID=w.REQUESTING_THREAD_ID
JOIN performance_schema.threads b ON b.THREAD_ID=w.BLOCKING_THREAD_ID
WHERE l.OBJECT_SCHEMA=DATABASE() AND l.OBJECT_NAME='device' AND l.LOCK_STATUS='WAITING'
AND r.PROCESSLIST_ID=? AND b.PROCESSLIST_ID=?`, waiter, blocker).Scan(&count).Error
		if err != nil {
			t.Fatal("设备锁等待观测失败", err)
		}
		if count > 0 {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("第二事务未等待设备锁即结束: %v", err)
		case <-ctx.Done():
			t.Fatal("未观察到设备锁等待", ctx.Err())
		case <-ticker.C:
		}
	}
}

// gatewayConnectionID 固定连接池为单连接并获取身份，供真实锁观察精确匹配等待双方。
func gatewayConnectionID(t *testing.T, db *gorm.DB) uint64 {
	t.Helper()
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	var id uint64
	if err := db.Raw("SELECT CONNECTION_ID()").Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}
