//go:build integration

package testfixture

import (
	"context"
	"os"
	"testing"
	"time"

	"chargepilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TestOutboxCleanupDoesNotLockOtherFixtures 在真实两连接中持有清理事务，另一连接仍能锁住无关夹具。
// 两条私有已发布记录按全部主键字段清理，不进入领取队列或改变任何资金状态。
func TestOutboxCleanupDoesNotLockOtherFixtures(t *testing.T) {
	open := func() *gorm.DB {
		t.Helper()
		raw := os.Getenv("TEST_MYSQL_URL")
		if raw == "" {
			t.Fatal("TEST_MYSQL_URL required")
		}
		pool, err := dbconn.Open(context.Background(), raw)
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
	first, second := open(), open()
	target := uuid.NewString()
	// row 是清理锁测试写入 event_outbox 的本测试已发布记录，回填真实主键并携清理载荷；不进入领取队列或执行投递。
	type row struct {
		ID           uint64 `gorm:"primaryKey"` // 实际插入回填的队列主键
		EventID      string // 本夹具独有事件标识
		Stream       string // 私有测试流，不执行投递
		EnvelopeJSON string // 清理载荷
		Status       string // 已发布状态，不进入后台领取
	}
	rows := []row{
		{EventID: uuid.NewString(), Stream: "fixture", EnvelopeJSON: `{"fixture":"unrelated"}`, Status: "published"},
		{EventID: uuid.NewString(), Stream: "fixture", EnvelopeJSON: `{"fixture":"` + target + `"}`, Status: "published"},
	}
	for i := range rows {
		if err := first.Table("event_outbox").Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
		id := rows[i].ID
		t.Cleanup(func() {
			if err := first.Table("event_outbox").Where("id=?", id).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := first.Table("event_outbox").Where("id=?", id).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("remaining fixture=%d err=%v", n, err)
			}
		})
	}
	tx := first.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := DeleteOutbox(tx, "envelope_json LIKE ?", "%"+target+"%"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var other row
	if err := second.WithContext(ctx).Table("event_outbox").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", rows[0].ID).Take(&other).Error; err != nil || other.ID != rows[0].ID || other.EventID != rows[0].EventID {
		t.Fatalf("unrelated fixture blocked or changed: id=%d err=%v", other.ID, err)
	}
	var n int64
	if err := tx.Table("event_outbox").Where("id=?", rows[1].ID).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("owned fixture not deleted: n=%d err=%v", n, err)
	}
}
