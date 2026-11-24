//go:build integration

package device_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"wattharbor/internal/ops"
	"wattharbor/internal/testfixture"
)

// deviceAuditKey 保存审计记录的完整分区主键，仅用于本测试的精确清理和锁定位。
type deviceAuditKey struct {
	ID           uint64    // 自增记录 ID，与月份共同定位唯一审计行。
	CreatedMonth time.Time // 数据库保存的分区月份，删除时使用读取值。
}

// cleanupDeviceAudits 在十秒内按本测试请求号定位完整主键，再逐行删除审计；空集合不执行 SQL。
// 先等待本测试写入结束，删除保留请求号条件，调用方核对零残留。
func cleanupDeviceAudits(ctx context.Context, db *gorm.DB, requestIDs []string) error {
	if len(requestIDs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var rows []deviceAuditKey
	if err := db.WithContext(ctx).Table("audit_log").Select("id,created_month").Where("request_id IN ?", requestIDs).
		Order("id,created_month").Find(&rows).Error; err != nil {
		return fmt.Errorf("locate device fixture audits: %w", err)
	}
	for _, row := range rows {
		if err := db.WithContext(ctx).Table("audit_log").Where("id=? AND created_month=? AND request_id IN ?", row.ID, row.CreatedMonth, requestIDs).
			Delete(map[string]any{}).Error; err != nil {
			return fmt.Errorf("delete device fixture audit %d: %w", row.ID, err)
		}
	}
	return nil
}

// TestDeviceAuditCleanupDoesNotWaitForOtherRequest 验证另一事务锁住无关审计时，仍可删除本测试审计。
// 使用两条真库连接和 RR 隔离，保留无关记录，不靠睡眠排序。
func TestDeviceAuditCleanupDoesNotWaitForOtherRequest(t *testing.T) {
	cleaner, locker := importDB(t, "TEST_MYSQL_URL"), importDB(t, "TEST_MYSQL_URL")
	otherRequest, ownRequest := uuid.NewString(), uuid.NewString()
	requests := []string{otherRequest, ownRequest}
	defer func() {
		if err := cleanupDeviceAudits(context.Background(), cleaner, requests); err != nil {
			t.Error(err)
		}
		var remaining int64
		if err := cleaner.Table("audit_log").Where("request_id IN ?", requests).Count(&remaining).Error; err != nil || remaining != 0 {
			t.Errorf("fixture audit remaining=%d: %v", remaining, err)
		}
	}()
	now := time.Now().UTC()
	actor := testfixture.NewID()
	for _, request := range requests {
		entry := ops.AuditEntryInput{
			ActorID: actor, Module: "fixture", Action: "cleanup", RequestID: request,
			BeforeJSON: []byte("null"), AfterJSON: []byte("null"),
		}
		if err := ops.InsertAudit(cleaner, entry, now); err != nil {
			t.Fatal(err)
		}
	}
	var other deviceAuditKey
	if err := cleaner.Table("audit_log").Select("id,created_month").Where("request_id=?", otherRequest).Take(&other).Error; err != nil {
		t.Fatal(err)
	}
	lockTx := locker.WithContext(t.Context()).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if lockTx.Error != nil {
		t.Fatal(lockTx.Error)
	}
	defer lockTx.Rollback()
	var locked deviceAuditKey
	lockQuery := lockTx.Raw("SELECT id,created_month FROM audit_log WHERE id=? AND created_month=? FOR UPDATE", other.ID, other.CreatedMonth).Scan(&locked)
	if lockQuery.Error != nil || lockQuery.RowsAffected != 1 || locked.ID != other.ID {
		t.Fatalf("lock unrelated audit affected=%d: %v", lockQuery.RowsAffected, lockQuery.Error)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	cleanupTx := cleaner.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if cleanupTx.Error != nil {
		t.Fatal(cleanupTx.Error)
	}
	defer cleanupTx.Rollback()
	if err := cleanupDeviceAudits(ctx, cleanupTx, []string{ownRequest}); err != nil {
		t.Fatalf("own fixture cleanup waited for unrelated audit lock: %v", err)
	}
	var ownRemaining, otherRemaining int64
	if err := cleanupTx.Table("audit_log").Where("request_id=?", ownRequest).Count(&ownRemaining).Error; err != nil || ownRemaining != 0 {
		t.Fatalf("own audit remaining=%d: %v", ownRemaining, err)
	}
	if err := cleanupTx.Table("audit_log").Where("id=? AND created_month=? AND request_id=?", other.ID, other.CreatedMonth, otherRequest).
		Count(&otherRemaining).Error; err != nil || otherRemaining != 1 {
		t.Fatalf("unrelated locked audit remaining=%d: %v", otherRemaining, err)
	}
	if err := cleanupTx.Commit().Error; err != nil {
		t.Fatal(err)
	}
}
