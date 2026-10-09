//go:build integration

package export

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"chargepilot/internal/ops"
	"gorm.io/gorm"
)

// cleanupExportAudits 先定位本测试审计的完整主键，再逐行删除，避免范围锁影响其他操作者。
// 所有写入退出后清理，保留操作者条件并检查零残留。
func cleanupExportAudits(db *gorm.DB, actor uint64) error {
	var rows []struct {
		ID           uint64    // 本测试夹具审计主键
		CreatedMonth time.Time // 分区主键月份
	}
	if err := db.Table("audit_log").Select("id,created_month").Where("actor_id=?", actor).
		Order("id,created_month").Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := db.Table("audit_log").Where("id=? AND created_month=? AND actor_id=?", row.ID, row.CreatedMonth, actor).
			Delete(map[string]any{}).Error; err != nil {
			return err
		}
	}
	var remaining int64
	if err := db.Table("audit_log").Where("actor_id=?", actor).Count(&remaining).Error; err != nil {
		return err
	}
	if remaining != 0 {
		return fmt.Errorf("export fixture audit remaining=%d", remaining)
	}
	return nil
}

// TestExportAuditCleanupDoesNotLockOtherActorGap 验证完整主键清理只锁目标行，相邻操作者仍可插入审计。
func TestExportAuditCleanupDoesNotLockOtherActorGap(t *testing.T) {
	s, _, actor, station := exportFixture(t)
	other := secondExportDB(t, actor, station)
	now := time.Now().UTC()
	entry := ops.AuditEntryInput{ActorID: actor, Module: "fixture", Action: "cleanup", BeforeJSON: []byte("null"), AfterJSON: []byte("null")}
	if err := ops.InsertAudit(s.DB, entry, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanupExportAudits(s.DB, actor-1); err != nil {
			t.Error(err)
		}
	})
	tx := s.DB.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := cleanupExportAudits(tx, actor); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	entry.ActorID = actor - 1
	if err := ops.InsertAudit(other.WithContext(ctx), entry, now); err != nil {
		t.Fatalf("unrelated actor insert blocked by fixture cleanup: %v", err)
	}
	var remaining int64
	if err := tx.Table("audit_log").Where("actor_id=?", actor).Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatalf("own audit remaining=%d: %v", remaining, err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
}
