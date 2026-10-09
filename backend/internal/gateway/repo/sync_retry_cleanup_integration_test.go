//go:build integration

package repo

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"chargepilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestSyncRetryCleanupPreservesPendingAndDeletesFinished 验证成功标记原子清理、迟到失败不重建，及有上限历史清理。
// 老的未完成review/retry保留；老的完成/缺源清理，新历史保留。仅扫描清理本测试完整身份。
func TestSyncRetryCleanupPreservesPendingAndDeletesFinished(t *testing.T) {
	db, other := syncTestConn(t), syncTestConn(t)
	ctx := context.Background()
	base := testfixture.NewID()
	commands := make([]string, 5)
	keys := make([]string, 5)
	ids := make([]uint64, 5)
	for i := range ids {
		ids[i] = base + uint64(i)
		commands[i] = uuid.NewString()
		keys[i] = strconv.FormatUint(ids[i], 10)
	}
	t.Cleanup(func() {
		for _, q := range []struct {
			table, filter string
			value         any
		}{
			{"sync_delivery_retry", "kind='start' AND delivery_key IN ?", commands},
			{"sync_delivery_retry", "kind='end' AND delivery_key IN ?", keys},
			{"charge_command", "command_id IN ?", commands},
			{"device_event", "id IN ?", ids},
		} {
			if err := db.Table(q.table).Where(q.filter, q.value).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := db.Table(q.table).Where(q.filter, q.value).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("%s residual=%d err=%v", q.table, n, err)
			}
		}
	})
	for i := 0; i < 3; i++ {
		if err := db.Table("charge_command").Create(map[string]any{"command_id": commands[i], "stop_command_id": uuid.NewString(), "charge_order_id": ids[i], "payment_order_id": ids[i], "order_no": commands[i], "user_id": base, "device_id": commands[0], "port_no": 1, "port_code": commands[0], "status": "acked", "result_reported": i == 2}).Error; err != nil {
			t.Fatal(err)
		}
		row := map[string]any{"id": ids[i], "event_key": uuid.NewString(), "protocol_name": "dc589", "device_id": commands[0], "event_type": "charge_end", "event_json": `{"ConsumerType":2}`, "received_at": time.Now().UTC()}
		if i == 2 {
			row["processed_at"] = time.Now().UTC()
		}
		if err := db.Table("device_event").Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"start", "end"} {
		key := commands[0]
		if kind == "end" {
			key = keys[0]
		}
		if err := DeferSyncDelivery(ctx, db, kind, key, "retry"); err != nil {
			t.Fatal(err)
		}
		// 注入清理存储失败：成功标记必须回滚，退避仍保留。
		failure := fmt.Errorf("private cleanup failure")
		callback := "test:cleanup-" + uuid.NewString()
		if err := db.Callback().Delete().Before("gorm:delete").Register(callback, func(q *gorm.DB) {
			if q.Statement.Table == "sync_delivery_retry" {
				q.AddError(failure)
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if db.Callback().Delete().Get(callback) != nil {
				db.Callback().Delete().Remove(callback)
			}
		})
		mark := func() (int, error) {
			if kind == "start" {
				return MarkStartResultsReported(ctx, db, []string{key})
			}
			return MarkDeviceEventsProcessed(ctx, db, []uint64{ids[0]})
		}
		if n, err := mark(); err == nil {
			t.Fatalf("cleanup failure committed mark: %d %v", n, err)
		}
		db.Callback().Delete().Remove(callback)
		if pending, err := syncDeliveryPending(other, kind, key); err != nil || !pending {
			t.Fatalf("mark not rolled back: %v %v", pending, err)
		}
		if n, err := mark(); err != nil || n != 1 {
			t.Fatalf("mark: %d %v", n, err)
		}
		if err := DeferSyncDelivery(ctx, other, kind, key, "retry"); err != nil {
			t.Fatal(err)
		}
		var n int64
		if err := db.Table("sync_delivery_retry").Where("kind=? AND delivery_key=?", kind, key).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("late retry recreated: %d %v", n, err)
		}
		for i := 1; i < 5; i++ {
			key := commands[i]
			if kind == "end" {
				key = keys[i]
			}
			old := time.Now().UTC().Add(-40 * 24 * time.Hour)
			if i == 1 {
				old = old.Add(-time.Hour)
			}
			if i == 4 {
				old = time.Now().UTC()
			}
			if err := db.Table("sync_delivery_retry").Create(map[string]any{"kind": kind, "delivery_key": key, "state": "review", "last_reason": "unknown", "updated_at": old}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	// 私有查询范围保留真实过期/源状态筛选；老review排在最前不能占满limit阻塞可清理项。
	scoped := db.Scopes(func(q *gorm.DB) *gorm.DB {
		if q.Statement.Table == "sync_delivery_retry" {
			return q.Where("delivery_key IN ?", append(commands, keys...))
		}
		return q
	})
	for _, round := range []struct{ limit, want int }{{1, 1}, {500, 3}, {500, 0}} {
		n, err := PurgeSyncDeliveryRetries(ctx, scoped, time.Now().UTC().Add(-30*24*time.Hour), round.limit)
		if err != nil || n != round.want {
			t.Fatalf("purge=%d want=%d err=%v", n, round.want, err)
		}
	}
	for _, kind := range []string{"start", "end"} {
		var remaining int64
		if err := db.Table("sync_delivery_retry").Where("kind=? AND delivery_key IN ?", kind, append(commands, keys...)).Count(&remaining).Error; err != nil || remaining != 2 {
			t.Fatalf("pending/fresh removed: %d %v", remaining, err)
		}
	}
	// 候选读取后由另一连接刷新时间；删除时必须重查，不能清掉刚更新的元数据。
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	if err := db.Table("sync_delivery_retry").Create(map[string]any{"kind": "start", "delivery_key": commands[2], "updated_at": old}).Error; err != nil {
		t.Fatal(err)
	}
	callback := "test:refresh-retry-" + uuid.NewString()
	refreshed := false
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(q *gorm.DB) {
		if q.Statement.Table != "sync_delivery_retry" || refreshed {
			return
		}
		refreshed = true
		if err := other.Table("sync_delivery_retry").Where("kind='start' AND delivery_key=?", commands[2]).Update("updated_at", time.Now().UTC()).Error; err != nil {
			q.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if db.Callback().Query().Get(callback) != nil {
			db.Callback().Query().Remove(callback)
		}
	})
	if n, err := PurgeSyncDeliveryRetries(ctx, scoped, time.Now().UTC().Add(-30*24*time.Hour), 1); err != nil || n != 0 || !refreshed {
		t.Fatalf("concurrent refresh deleted=%d refreshed=%v err=%v", n, refreshed, err)
	}
	db.Callback().Query().Remove(callback)
	var remaining int64
	if err := db.Table("sync_delivery_retry").Where("kind='start' AND delivery_key=?", commands[2]).Count(&remaining).Error; err != nil || remaining != 1 {
		t.Fatalf("refreshed retry lost: %d %v", remaining, err)
	}
}
