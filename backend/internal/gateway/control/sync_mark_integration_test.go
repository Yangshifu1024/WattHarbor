//go:build integration

package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"wattharbor/internal/gateway/session"

	"github.com/google/uuid"
	"gorm.io/gorm"
	gatewaycontract "wattharbor/internal/contracts/gateway"
	gatewayclient "wattharbor/internal/integration/gateway"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
	"wattharbor/internal/testfixture"
)

// TestSyncMarkBatchRollsBackAfterCancellation 验证实际注册端点的首行写入后取消会回滚整批，恢复和重复身份仍按实际影响行数返回。
// 只提交随机命令／事件夹具，请求退出后按全部主键字段清理并核对零残留；不调用设备或中央资金接口。
func TestSyncMarkBatchRollsBackAfterCancellation(t *testing.T) {
	for _, table := range []string{"charge_command", "device_event"} {
		t.Run(table, func(t *testing.T) {
			db := gatewayContractDB(t)
			device := "MARK-" + uuid.NewString()
			ids := []uint64{testfixture.NewID(), testfixture.NewID() + 1}
			commands := []string{uuid.NewString(), uuid.NewString()}
			key, filter := "id", "processed_at IS NOT NULL"
			var identities any = ids
			path := gatewaycontract.MarkDeviceEventsPath
			var body any = gatewaycontract.MarkDeviceEventsRequest{IDs: []uint64{ids[0], ids[0], ids[1]}}
			if table == "charge_command" {
				key, filter, identities = "command_id", "result_reported = TRUE", commands
				path = gatewaycontract.MarkStartResultsPath
				body = gatewaycontract.MarkStartResultsRequest{CommandIDs: []string{commands[0], commands[0], commands[1]}}
			}
			t.Cleanup(func() {
				if err := db.Table(table).Where(key+" IN ?", identities).Delete(map[string]any{}).Error; err != nil {
					t.Error("fixture cleanup", err)
					return
				}
				var remaining int64
				if err := db.Table(table).Where(key+" IN ?", identities).Count(&remaining).Error; err != nil || remaining != 0 {
					t.Errorf("fixture remaining=%d err=%v", remaining, err)
				}
			})
			for i := range 2 {
				row := map[string]any{"id": ids[i], "event_key": uuid.NewString(), "protocol_name": "dc589", "device_id": device, "event_type": "heartbeat", "event_json": `{}`, "received_at": time.Now().UTC()}
				if table == "charge_command" {
					row = map[string]any{"command_id": commands[i], "stop_command_id": uuid.NewString(), "charge_order_id": ids[i], "payment_order_id": ids[i], "order_no": "MARK-" + commands[i], "user_id": ids[i], "device_id": device, "port_no": 1, "port_code": device, "status": "acked"}
				}
				if err := db.Table(table).Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			router := httpapi.NewRouter()
			(DeviceEventAPI{DB: db, ServiceToken: "sync-mark-test"}).Register(router)
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			call := func(ctx context.Context) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(data))).WithContext(ctx)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Service-Token", "sync-mark-test")
				out := httptest.NewRecorder()
				router.ServeHTTP(out, req)
				return out
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			callback := "test:sync-mark-cancel-" + uuid.NewString()
			writes := 0
			// 等待单行更新的默认事务回调结束，再取消；缺失外层整批事务时，首行会已提交，测试能检出部分更新。
			if err := db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register(callback, func(update *gorm.DB) {
				if update.Statement.Table == table && update.Error == nil && update.RowsAffected == 1 {
					writes++
					cancel()
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer db.Callback().Update().Remove(callback)
			out := call(ctx)
			if out.Code != http.StatusServiceUnavailable || writes != 1 {
				t.Fatalf("cancel after first successful SQL write: status=%d writes=%d body=%s", out.Code, writes, out.Body.String())
			}
			var marked int64
			if err := db.Table(table).Where(key+" IN ?", identities).Where(filter).Count(&marked).Error; err != nil || marked != 0 {
				t.Fatalf("canceled batch partially committed: marked=%d err=%v", marked, err)
			}
			if err := db.Callback().Update().Remove(callback); err != nil {
				t.Fatal(err)
			}
			for _, want := range []int{2, 0} {
				out = call(context.Background())
				var reply struct {
					Code int                       `json:"code"` // HTTP 业务码。
					Data gatewaycontract.MarkReply `json:"data"` // 实际影响行数。
				}
				if err := json.Unmarshal(out.Body.Bytes(), &reply); err != nil || out.Code != http.StatusOK || reply.Code != 0 || reply.Data.Marked != want {
					t.Fatalf("recovery/replay %d: status=%d reply=%+v err=%v body=%s", want, out.Code, reply, err, out.Body.String())
				}
			}
			if err := db.Table(table).Where(key+" IN ?", identities).Where(filter).Count(&marked).Error; err != nil || marked != 2 {
				t.Fatalf("recovered batch state: marked=%d err=%v", marked, err)
			}
		})
	}
}

// TestLateReleaseCannotFreeNewOrder 验证真实端口在释放重放后被新订单占用时，迟到旧回执保持 409 且不改新占用。
// 夹具仅在外层事务中可见并回滚，不将模拟的订单归属写入当作中央结算或设备执行证据。
func TestLateReleaseCannotFreeNewOrder(t *testing.T) {
	db := gatewayContractDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := testfixture.NewID()
	device, oldOrder, newOrder := "PORT-"+uuid.NewString(), "OLD-"+uuid.NewString(), "NEW-"+uuid.NewString()
	if err := tx.Table("device_port").Create(map[string]any{"id": id, "device_id": device, "port_no": 1, "port_code": device, "status": "charging", "current_order_id": oldOrder}).Error; err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter()
	(DeviceEventAPI{DB: tx, Devices: &session.Registry{}, ServiceToken: "release-test"}).Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "release-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := gatewaycontract.ReleasePortRequest{PortID: id, OrderNo: oldOrder}
	for i := range 2 {
		reply, err := client.ReleasePort(ctx, request)
		if err != nil || !reply.Released || reply.Already != (i == 1) {
			t.Fatalf("original release/replay: reply=%+v err=%v", reply, err)
		}
	}
	if err := tx.Table("device_port").Where("id = ?", id).Updates(map[string]any{"status": "charging", "current_order_id": newOrder}).Error; err != nil {
		t.Fatal(err)
	}
	_, err = client.ReleasePort(ctx, request)
	var rejected *serviceclient.HTTPError
	if !errors.As(err, &rejected) || rejected.StatusCode != http.StatusConflict {
		t.Fatalf("late old release accepted: %v", err)
	}
	var unchanged int64
	if err := tx.Table("device_port").Where("id = ? AND status = 'charging' AND current_order_id = ?", id, newOrder).Count(&unchanged).Error; err != nil || unchanged != 1 {
		t.Fatalf("new owner changed: count=%d err=%v", unchanged, err)
	}
	request.PortID++
	_, err = client.ReleasePort(ctx, request)
	if !errors.As(err, &rejected) || rejected.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("missing port lost original read failure: %v", err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("device_port").Where("id = ?", id).Count(&unchanged).Error; err != nil || unchanged != 0 {
		t.Fatalf("fixture remains: count=%d err=%v", unchanged, err)
	}
}
