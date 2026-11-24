//go:build integration

package repo

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"wattharbor/internal/platform/dbconn"
	"wattharbor/internal/protocol"
	"wattharbor/internal/testfixture"
)

// syncTestConn 打开独立 gateway 连接，使用显式测试配置；失败直接终止，不提供跳过路径。
func syncTestConn(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_URL")
	if raw == "" {
		t.Fatal("TEST_MYSQL_URL required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal("invalid test URL")
	}
	// TEST_MYSQL_URL 来自既有开发入口；只替换 schema，不读取或输出凭据。
	parsed.Path = "/gateway_db"
	pool, err := dbconn.Open(context.Background(), parsed.String())
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

// TestSyncRetryPersistsFairnessAfterRestart 以51条独立记录验证首批异常退避后，另一连接可以读取后续事件。
// 仅清理本测试完整主键及元数据，并确认0残留；不修改开发业务记录。
func TestSyncRetryPersistsFairnessAfterRestart(t *testing.T) {
	writer, reader := syncTestConn(t), syncTestConn(t)
	ctx := context.Background()
	id := testfixture.NewID()
	device := "sync-" + uuid.NewString()
	ids := make([]uint64, 51)
	commands := make([]string, 51)
	keys := make([]string, 51)
	for i := range ids {
		ids[i] = id + uint64(i)
		commands[i] = uuid.NewString()
		keys[i] = strconv.FormatUint(ids[i], 10)
	}
	t.Cleanup(func() {
		for _, q := range []struct {
			table, where string
			args         []any
		}{
			{"sync_delivery_retry", "kind='start' AND delivery_key IN ?", []any{commands}},
			{"sync_delivery_retry", "kind='end' AND delivery_key IN ?", []any{keys}},
			{"charge_command", "command_id IN ?", []any{commands}},
			{"device_event", "id IN ?", []any{ids}},
		} {
			if err := writer.Table(q.table).Where(q.where, q.args...).Delete(nil).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := writer.Table(q.table).Where(q.where, q.args...).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("%s remaining=%d err=%v", q.table, n, err)
			}
		}
	})
	for i := range ids {
		readyAt := time.Now().UTC().Add(-time.Hour).Add(time.Duration(i) * time.Second)
		event, _ := json.Marshal(protocol.Event{Type: protocol.ChargeEnd, ConsumerType: 2, DeviceID: device, OrderNumber: keys[i]})
		hash := sha256.Sum256([]byte(commands[i]))
		if err := writer.Table("device_event").Create(map[string]any{"id": ids[i], "event_key": fmt.Sprintf("%x", hash), "protocol_name": "dc589", "device_id": device, "event_type": "charge_end", "event_json": string(event), "received_at": readyAt}).Error; err != nil {
			t.Fatal(err)
		}
		if err := writer.Table("charge_command").Create(map[string]any{"command_id": commands[i], "stop_command_id": uuid.NewString(), "charge_order_id": ids[i], "payment_order_id": ids[i], "order_no": device + strconv.Itoa(i), "user_id": id, "device_id": device, "port_no": 1, "port_code": device, "status": "rejected", "result_code": 254, "ack_at": readyAt, "updated_at": readyAt}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Scope 仅限制本测试数据，不替代真实查询的排序、窗口与持久到期条件。
	scope := func(db *gorm.DB) *gorm.DB {
		return db.Scopes(func(q *gorm.DB) *gorm.DB {
			if q.Statement.Table == "charge_command" {
				return q.Where("charge_command.device_id=?", device)
			}
			if q.Statement.Table == "device_event" {
				return q.Where("device_event.device_id=?", device)
			}
			return q
		})
	}
	rows, err := ListStartResults(ctx, scope(writer), "")
	if err != nil || len(rows) != 50 {
		t.Fatalf("start len=%d err=%v", len(rows), err)
	}
	for _, r := range rows {
		if err := DeferSyncDelivery(ctx, writer, "start", r.CommandID, "unknown"); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := ListStartResults(ctx, scope(reader), "")
	if err != nil || len(restarted) != 1 || restarted[0].CommandID != commands[50] {
		t.Fatalf("restart start=%+v err=%v", restarted, err)
	}
	ends, err := ListUnprocessedEndEvents(ctx, scope(writer))
	if err != nil || len(ends) != 50 {
		t.Fatalf("end len=%d err=%v", len(ends), err)
	}
	for _, r := range ends {
		if err := DeferSyncDelivery(ctx, writer, "end", strconv.FormatUint(r.ID, 10), "dependency"); err != nil {
			t.Fatal(err)
		}
	}
	after, err := ListUnprocessedEndEvents(ctx, scope(reader))
	if err != nil || len(after) != 1 || after[0].ID != ids[50] {
		t.Fatalf("restart end=%+v err=%v", after, err)
	}
	var n int64
	if err := writer.Table("charge_command").Where("command_id IN ? AND (status<>'rejected' OR result_code<>254 OR result_reported<>FALSE)", commands).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("unknown outcome changed n=%d err=%v", n, err)
	}
	if err := writer.Table("device_event").Where("id IN ? AND processed_at IS NOT NULL", ids).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("failed event processed n=%d err=%v", n, err)
	}
	// 将旧重试置为更早的到期时间，新投递仍在窗口内；重启连接必须先取到旧重试。
	for _, kind := range []string{"start", "end"} {
		key := commands[0]
		if kind == "end" {
			key = keys[0]
		}
		if err := writer.Table("sync_delivery_retry").Where("kind=? AND delivery_key=?", kind, key).Update("next_attempt_at", time.Now().UTC().Add(-2*time.Hour)).Error; err != nil {
			t.Fatal(err)
		}
	}
	startDue, err := ListStartResults(ctx, scope(reader), "")
	if err != nil || len(startDue) != 2 || startDue[0].CommandID != commands[0] {
		t.Fatalf("due start=%+v err=%v", startDue, err)
	}
	endDue, err := ListUnprocessedEndEvents(ctx, scope(reader))
	if err != nil || len(endDue) != 2 || endDue[0].ID != ids[0] {
		t.Fatalf("due end=%+v err=%v", endDue, err)
	}
}
