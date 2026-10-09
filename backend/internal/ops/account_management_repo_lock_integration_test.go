//go:build integration

package ops

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"chargepilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// roleSerialLockConnection 为 t 打开原 TEST_MYSQL_URL 的真实池，不输出含凭据的 URL。
// 返回 GORM 连接，由测试在事务及 goroutine 退出后关闭；缺配置或连接失败直接失败，不跳过。
func roleSerialLockConnection(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_URL")
	if raw == "" {
		t.Fatal("TEST_MYSQL_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := dbconn.Open(ctx, raw)
	if err != nil {
		t.Fatalf("role lock test connection: %v", err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	db, err := dbconn.WrapGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// roleSerialWait 是性能库记录的真实 role 锁等待，保留索引与锁模式用于复现证据。
// 它不模拟事务、闸门或锁，等待双方通过各自固定连接 ID 识别。
type roleSerialWait struct {
	IndexName string `gorm:"column:index_name"` // 原 role 索引名，旧 uk_code 或新 uk_active_code。
	LockMode  string `gorm:"column:lock_mode"`  // MySQL 实际等待锁模式，唯一命中应为记录锁。
	LockData  string `gorm:"column:lock_data"`  // 原被锁键值，仅输出角色编码与主键，不含凭据。
}

// awaitRoleSerialWait 在 ctx 期限内只读性能库，直到 waiter 真正等待 blocker 的原角色锁。
// ticker 仅限查询频次，执行次序由 data_lock_waits 的真实记录决定；无权限、提前返回或期限到达都失败。
func awaitRoleSerialWait(t *testing.T, ctx context.Context, observer *gorm.DB, schema string, waiter, blocker uint64, done <-chan error) roleSerialWait {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var rows []roleSerialWait
		err := observer.WithContext(ctx).Raw(`SELECT l.INDEX_NAME AS index_name,l.LOCK_MODE AS lock_mode,l.LOCK_DATA AS lock_data
FROM performance_schema.data_lock_waits AS w
JOIN performance_schema.threads AS r ON r.THREAD_ID=w.REQUESTING_THREAD_ID
JOIN performance_schema.threads AS b ON b.THREAD_ID=w.BLOCKING_THREAD_ID
JOIN performance_schema.data_locks AS l ON l.ENGINE=w.ENGINE AND l.ENGINE_LOCK_ID=w.REQUESTING_ENGINE_LOCK_ID
WHERE r.PROCESSLIST_ID=? AND b.PROCESSLIST_ID=?
AND l.OBJECT_SCHEMA=? AND l.OBJECT_NAME='role' AND l.LOCK_TYPE='RECORD' AND l.LOCK_STATUS='WAITING'`, waiter, blocker, schema).Scan(&rows).Error
		if err != nil {
			t.Fatalf("actual role lock observation failed; isolated test account needs SELECT on data_lock_waits/data_locks/threads: %v", err)
		}
		for _, row := range rows {
			if (row.IndexName == "uk_code" || row.IndexName == "uk_active_code") && strings.Contains(row.LockData, "customer_admin") {
				t.Logf("actual waiter=%d blocker=%d index=%s mode=%s data=%s", waiter, blocker, row.IndexName, row.LockMode, row.LockData)
				return row
			}
		}
		select {
		case err := <-done:
			t.Fatalf("role waiter returned before actual lock wait: %v", err)
		case <-ctx.Done():
			t.Fatalf("actual role lock wait not observed: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

// TestCustomerAdminSerialLockDoesNotDeadlockWithRoleInsert 用两条真实事务连接复现等待角色锁后插入前驱编码的次序。
// 性能库确认 B 等待 A 后，A 调用真实 InsertRole 并提交，B 必须正常取得原串行点；私有角色按完整主键清理。
// 不改隔离级别、不重试、不伪造等待，也不修改内置角色或将全仓测试串行化。
func TestCustomerAdminSerialLockDoesNotDeadlockWithRoleInsert(t *testing.T) {
	for _, mode := range []string{"insert", "cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			first, second := roleSerialLockConnection(t), roleSerialLockConnection(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			var waitCtx context.Context
			var cancelWait context.CancelFunc
			if mode == "deadline" {
				waitCtx, cancelWait = context.WithTimeout(ctx, 5*time.Second)
			} else {
				waitCtx, cancelWait = context.WithCancel(ctx)
			}
			defer cancelWait()
			a := first.WithContext(ctx).Begin()
			if a.Error != nil {
				t.Fatal(a.Error)
			}
			b := second.WithContext(waitCtx).Begin()
			if b.Error != nil {
				_ = a.Rollback()
				t.Fatal(b.Error)
			}
			done := make(chan error, 1)
			var workers sync.WaitGroup
			var insertedID uint64
			t.Cleanup(func() {
				cancelWait()
				if err := a.Rollback().Error; err != nil && !errors.Is(err, sql.ErrTxDone) {
					t.Error(err)
				}
				workers.Wait()
				if err := b.Rollback().Error; err != nil && !errors.Is(err, sql.ErrTxDone) {
					t.Error(err)
				}
				if insertedID != 0 {
					cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cleanupCancel()
					if err := first.WithContext(cleanupCtx).Table("role").Where("id=?", insertedID).Delete(map[string]any{}).Error; err != nil {
						t.Error(err)
					}
					var left int64
					if err := first.WithContext(cleanupCtx).Table("role").Where("id=?", insertedID).Count(&left).Error; err != nil || left != 0 {
						t.Errorf("private serial role remains=%d err=%v", left, err)
					}
				}
			})
			var blocker, waiter uint64
			var schema string
			if err := a.Raw("SELECT CONNECTION_ID()").Scan(&blocker).Error; err != nil {
				t.Fatal(err)
			}
			if err := b.Raw("SELECT CONNECTION_ID()").Scan(&waiter).Error; err != nil {
				t.Fatal(err)
			}
			if blocker == 0 || waiter == 0 || blocker == waiter {
				t.Fatalf("transaction connection IDs are not independent: %d/%d", blocker, waiter)
			}
			if err := a.Raw("SELECT DATABASE()").Scan(&schema).Error; err != nil || schema == "" {
				t.Fatalf("test schema=%q err=%v", schema, err)
			}
			if err := (AccountRepo{DB: a}).LockCustomerAdminRole(a); err != nil {
				t.Fatal(err)
			}
			// 该合法私有编码比 customer_admin 小，且更接近边界，避免其他 custlow_ 夹具落在它与目标之间。
			code := "customer_admim_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			var intervening int64
			if err := a.Table("role").Where("code >= ? AND code < ?", code, "customer_admin").Count(&intervening).Error; err != nil || intervening != 0 {
				t.Fatalf("fixture code is not in the actual predecessor gap: rows=%d err=%v", intervening, err)
			}
			workers.Go(func() { done <- (AccountRepo{DB: b}).LockCustomerAdminRole(b) })
			awaitRoleSerialWait(t, ctx, a, schema, waiter, blocker, done)
			if mode == "insert" {
				var err error
				insertedID, err = (RoleRepo{DB: a}).InsertRole(a, code, "serial lock fixture", nil)
				if err != nil {
					t.Fatalf("role insert while actual waiter queued: %v", err)
				}
				if insertedID == 0 {
					t.Fatal("insert did not return actual role ID")
				}
				if err := a.Commit().Error; err != nil {
					t.Fatalf("role insert commit: %v", err)
				}
			} else if mode == "cancel" {
				cancelWait()
			}
			select {
			case err := <-done:
				if mode == "insert" && err != nil {
					t.Fatalf("role waiter after original owner insert/commit: %v", err)
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("role waiter cancel did not respond: %v", err)
				}
				if mode == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("role waiter deadline did not respond: %v", err)
				}
			case <-ctx.Done():
				t.Fatalf("managed role waiter did not finish: %v", ctx.Err())
			}
		})
	}
}
