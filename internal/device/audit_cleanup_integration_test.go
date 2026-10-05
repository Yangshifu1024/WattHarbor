//go:build integration

package device_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// deviceAuditKey 保存审计记录的完整分区主键，仅用于本测试的精确清理和锁定位。
type deviceAuditKey struct {
	ID           uint64    // 自增记录 ID，与月份共同定位唯一审计行。
	CreatedMonth time.Time // 数据库保存的分区月份，删除时使用原值。
}

// cleanupDeviceAudits 在 ctx 及最多十秒期限内清理 db 中 requestIDs 对应的测试审计。
// 先非锁枚举完整主键，再按固定主键顺序逐行删除并保留请求归属条件；空集合不执行 SQL。
// 返回定位或删除错误，不重试、不提交调用方事务；调用前须等待本 fixture 写入退出，调用方核对零残留。
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

// TestDeviceAuditCleanupDoesNotWaitForOtherRequest 用 t 管理两条独立真库连接和唯一请求造数。
// 明确 RR：另一事务完整主键锁住无关审计后，清理事务仍须在该锁未释放时删除自身请求并保留无关行。
// 原无索引 request_id 范围 DELETE 会等待无关主键锁直至期限；测试不使用 sleep，不证明生产资金逻辑。
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
	actor := uint64(now.UnixNano())
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
