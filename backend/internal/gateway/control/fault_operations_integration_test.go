//go:build integration

package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// faultEnvelopeReply 保留注册端点的原状态、消息及可空数据，用于指定持久操作回归。
type faultEnvelopeReply struct {
	Code    int             `json:"code"`    // 业务码。
	Message string          `json:"message"` // 阶段消息。
	Data    json.RawMessage `json:"data"`    // 数据，失败须为 null。
}

// callFaultEndpoint 经实际 TelemetryAPI.Register 发送携令牌请求，ctx 管理数据库取消，不启动异步请求。
func callFaultEndpoint(t *testing.T, db *gorm.DB, method, path, body string, ctx context.Context) (int, faultEnvelopeReply) {
	t.Helper()
	router := httpapi.NewRouter()
	(TelemetryAPI{DB: db, ServiceToken: "fault-operations-test"}).Register(router)
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Token", "fault-operations-test")
	out := httptest.NewRecorder()
	router.ServeHTTP(out, req)
	var reply faultEnvelopeReply
	if err := json.Unmarshal(out.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	return out.Code, reply
}

// TestFaultMarkKeepsTransactionAndReplay 验证真实批量更新后取消仍整批回滚，恢复和重复 ID 重放保持实际计数。
// 私有主键清理并检查零残留，不触发中央告警、资金或设备操作。
func TestFaultMarkKeepsTransactionAndReplay(t *testing.T) {
	db := gatewayContractDB(t)
	id, board := testfixture.NewID(), "FAULT-MARK-"+uuid.NewString()
	ids := []uint64{id, id + 1, id + 2, id + 3}
	at := time.Now().UTC().Truncate(time.Millisecond)
	old := at.Add(-time.Hour)
	t.Cleanup(func() {
		if err := db.Table("device_event").Where("id IN ?", ids).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
			return
		}
		var n int64
		if err := db.Table("device_event").Where("device_id = ?", board).Count(&n).Error; err != nil || n != 0 {
			t.Errorf("fixture remaining=%d err=%v", n, err)
		}
	})
	for i, key := range ids {
		var processed any
		kind, protocol := "fault", "dc589"
		if i == 1 {
			// 标记端点原来只按主键确认，不额外限定 fault 类型或协议。
			kind, protocol = "heartbeat", "other"
		}
		if i == 2 {
			processed = old
		}
		if err := db.Table("device_event").Create(map[string]any{"id": key, "event_key": uuid.NewString(), "protocol_name": protocol, "device_id": board, "event_type": kind, "event_json": `{}`, "received_at": at, "processed_at": processed}).Error; err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(contract.MarkDeviceFaultsRequest{IDs: []uint64{id, id, id + 1, id + 2, id + 4, 0}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook, writes := "test:fault-bulk-cancel-"+uuid.NewString(), 0
	// 在唯一批量 UPDATE 的默认事务回调后取消；去掉外层事务会先提交两行，不能被错误回复掩盖。
	if err := db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register(hook, func(update *gorm.DB) {
		if update.Statement.Table == "device_event" && update.Error == nil && update.RowsAffected == 2 {
			writes++
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Update().Remove(hook)
	status, reply := callFaultEndpoint(t, db, http.MethodPost, contract.MarkDeviceFaultsPath, string(data), ctx)
	var n int64
	if err := db.Table("device_event").Where("id IN ? AND processed_at IS NOT NULL", ids[:2]).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("canceled bulk update committed: marked=%d err=%v", n, err)
	}
	if status != 503 || reply.Code != 5003 || reply.Message != "处理标记暂不可写" || string(reply.Data) != "null" || writes != 1 {
		t.Fatalf("canceled bulk response: status=%d reply=%+v writes=%d", status, reply, writes)
	}
	if err := db.Callback().Update().Remove(hook); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{2, 0} {
		status, reply = callFaultEndpoint(t, db, http.MethodPost, contract.MarkDeviceFaultsPath, string(data), context.Background())
		var result contract.MarkDeviceFaultsReply
		if err := json.Unmarshal(reply.Data, &result); err != nil || status != 200 || reply.Code != 0 || result.Marked != want {
			t.Fatalf("mark recovery/replay: status=%d reply=%+v result=%+v err=%v want=%d", status, reply, result, err, want)
		}
	}
	if err := db.Table("device_event").Where("id = ? AND processed_at = ?", id+2, old).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("original processed time changed: count=%d err=%v", n, err)
	}
	if err := db.Table("device_event").Where("id = ? AND processed_at IS NULL", id+3).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("unrequested event advanced: count=%d err=%v", n, err)
	}
}

// TestHeartbeatReadKeepsWatermarkAndOrdering 验证键优先、严格时间／协议／设备过滤、同刻 ID 倒序和缺失／错误响应。
// 设备事件由外层事务回滚；本测试只核对心跳查询，不判断故障是否恢复。
func TestHeartbeatReadKeepsWatermarkAndOrdering(t *testing.T) {
	db := gatewayContractDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id, board, key := testfixture.NewID(), "HEARTBEAT-"+uuid.NewString(), uuid.NewString()
	at := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	latest := at.Add(time.Minute)
	for i, seed := range []struct {
		board    string    // 持久设备编号。
		protocol string    // 事件协议；只读取 dc589 心跳。
		kind     string    // 事件类型。
		at       time.Time // 接收时刻，支持同刻 ID 仲裁。
	}{
		{board, "dc589", "fault", at},
		{board, "dc589", "heartbeat", at},
		{board, "dc589", "heartbeat", latest},
		{board, "dc589", "heartbeat", latest},
		{board, "other", "heartbeat", latest.Add(time.Minute)},
		{board + "-other", "dc589", "heartbeat", latest.Add(time.Minute)},
		{board, "dc589", "fault", latest.Add(time.Minute)},
	} {
		eventKey := uuid.NewString()
		if i == 0 {
			eventKey = key
		}
		payload, err := json.Marshal(map[string]int{"winner": i})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Table("device_event").Create(map[string]any{"id": id + uint64(i), "event_key": eventKey, "protocol_name": seed.protocol, "device_id": seed.board, "event_type": seed.kind, "event_json": string(payload), "received_at": seed.at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	path := contract.LatestHeartbeatPath + board + "/latest-heartbeat"
	for _, query := range []string{"after_key=" + key + "&after=invalid", "after=" + url.QueryEscape(at.Format(time.RFC3339Nano))} {
		status, reply := callFaultEndpoint(t, tx, http.MethodGet, path+"?"+query, "", context.Background())
		var result contract.HeartbeatAfterKeyReply
		var payload struct {
			Winner int `json:"winner"` /* 回执载荷标识。 */
		}
		if err := json.Unmarshal(reply.Data, &result); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(result.Payload), &payload); err != nil {
			t.Fatal(err)
		}
		if status != 200 || reply.Code != 0 || !result.Found || result.AfterReceivedAt == nil || !result.AfterReceivedAt.Equal(at) || result.ReceivedAt == nil || !result.ReceivedAt.Equal(latest) || payload.Winner != 3 {
			t.Fatalf("heartbeat order/watermark: status=%d reply=%+v result=%+v payload=%+v", status, reply, result, payload)
		}
	}
	// 严格 > 排除同刻心跳；直接 after 回复保留偏移，只有 SQL 比较用 UTC。
	offset := latest.In(time.FixedZone("UTC+8", 8*60*60)).Format(time.RFC3339Nano)
	status, reply := callFaultEndpoint(t, tx, http.MethodGet, path+"?after="+url.QueryEscape(offset), "", context.Background())
	var result contract.HeartbeatAfterKeyReply
	if err := json.Unmarshal(reply.Data, &result); err != nil || status != 200 || reply.Code != 0 || result.Found || result.AfterReceivedAt == nil || !result.AfterReceivedAt.Equal(latest) || result.ReceivedAt != nil || result.Payload != "" || !strings.Contains(string(reply.Data), "+08:00") {
		t.Fatalf("strict no-newer/timezone: status=%d result=%+v data=%s err=%v", status, result, reply.Data, err)
	}
	for _, query := range []string{"after_key=missing&after=invalid", "after_key=" + key} {
		missingPath := path
		if query == "after_key="+key {
			missingPath = contract.LatestHeartbeatPath + board + "-other/latest-heartbeat"
		}
		status, reply = callFaultEndpoint(t, tx, http.MethodGet, missingPath+"?"+query, "", context.Background())
		result = contract.HeartbeatAfterKeyReply{}
		if err := json.Unmarshal(reply.Data, &result); err != nil || status != 200 || reply.Code != 0 || result.Found || result.AfterReceivedAt != nil || result.ReceivedAt != nil || result.Payload != "" {
			t.Fatalf("missing original watermark: status=%d result=%+v err=%v", status, result, err)
		}
	}
	for _, check := range []struct {
		query   string // 输入分支。
		status  int    // HTTP 结果。
		message string // 公开消息。
	}{
		{"after=invalid", 400, "after 须为 RFC 3339 时间"},
		{"after=0001-01-01T00:00:00Z", 503, "故障水印暂不可读取"},
	} {
		status, reply = callFaultEndpoint(t, tx, http.MethodGet, path+"?"+check.query, "", context.Background())
		if status != check.status || reply.Message != check.message || string(reply.Data) != "null" {
			t.Fatalf("watermark input: status=%d reply=%+v want=%+v", status, reply, check)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, reply = callFaultEndpoint(t, tx, http.MethodGet, path+"?after_key="+key, "", ctx)
	if status != 503 || reply.Code != 5003 || reply.Message != "心跳证据暂不可读取" || string(reply.Data) != "null" {
		t.Fatalf("watermark query canceled: status=%d reply=%+v", status, reply)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.Table("device_event").Where("device_id IN ?", []string{board, board + "-other"}).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("heartbeat fixture remaining=%d err=%v", n, err)
	}
}
