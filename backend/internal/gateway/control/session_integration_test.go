//go:build integration

package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// sessionDataReply 描述会话审计接口的数据，仅供注册端点回归断言。
type sessionDataReply struct {
	Items            []IdleSession `json:"items"`              // 候选连接信息。
	Count            int           `json:"count"`              // 列表数量。
	Closed           int64         `json:"closed"`             // 本次实际关闭数量。
	IdleThresholdSec int           `json:"idle_threshold_sec"` // 静默阈值秒数。
}

// sessionEnvelopeReply 保留错误时 data:null，不能把数据库失败当作空列表成功。
type sessionEnvelopeReply struct {
	Code    int               `json:"code"`    // 公开业务码。
	Message string            `json:"message"` // 公开消息。
	Data    *sessionDataReply `json:"data"`    // 成功数据，失败为 nil。
}

// callSessionEndpoint 通过实际 Register 发起携服务令牌的请求，返回 HTTP 状态及回执；ctx 控制持久操作取消。
func callSessionEndpoint(t *testing.T, api SessionAPI, method string, ctx context.Context) (int, sessionEnvelopeReply) {
	t.Helper()
	router := httpapi.NewRouter()
	api.Register(router)
	path := "/api/v1/internal/device-sessions"
	if method == http.MethodPost {
		path += "/cleanup-idle"
	}
	req := httptest.NewRequest(method, path, nil).WithContext(ctx)
	req.Header.Set("X-Service-Token", api.ServiceToken)
	out := httptest.NewRecorder()
	router.ServeHTTP(out, req)
	var reply sessionEnvelopeReply
	if err := json.Unmarshal(out.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	return out.Code, reply
}

// TestIdleSessionEndpointsKeepCompositeIdentity 验证真实候选排序、NULL 远端地址及完整 id/日期身份，清理不覆盖近期或正常关闭记录。
// 所有夹具和嵌套清理事务位于外层回滚事务，只验证持久审计，不关闭真实连接。
func TestIdleSessionEndpointsKeepCompositeIdentity(t *testing.T) {
	db := gatewayContractDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 不让全局清理端点接触非夹具的候选，独立测试库若存在遗留开放记录应明确失败。
	var existing int64
	if err := tx.Table("device_session").Where("ended_at IS NULL AND last_active_at < ?", time.Now().UTC().Add(-15*time.Minute)).Count(&existing).Error; err != nil || existing != 0 {
		t.Fatalf("non-fixture idle sessions: %d err=%v", existing, err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	previous := month.AddDate(0, -1, 0)
	id, device := testfixture.NewID(), "SESSION-"+uuid.NewString()
	oldSession, oldestSession := uuid.NewString(), uuid.NewString()
	for _, seed := range []struct {
		id      uint64    // 审计主键，跨日期可相同。
		month   time.Time // 完整分区日期。
		session string    // 设备连接身份。
		active  time.Time // 最后活动时刻。
		ended   any       // nil 为开放记录。
		reason  any       // 正常关闭原因，开放记录为 nil。
	}{
		{id, previous, oldSession, now.Add(-2 * time.Hour), nil, nil},
		{id, month, uuid.NewString(), now.Add(-time.Minute), nil, nil},
		{id + 1, month, oldestSession, now.Add(-3 * time.Hour), nil, nil},
		{id + 2, month, uuid.NewString(), now.Add(-4 * time.Hour), now.Add(-time.Hour), "normal"},
	} {
		if err := tx.Table("device_session").Create(map[string]any{"id": seed.id, "created_month": seed.month, "session_id": seed.session, "device_id": device, "protocol": "tcp", "remote_addr": nil, "started_at": seed.month, "last_active_at": seed.active, "ended_at": seed.ended, "close_reason": seed.reason}).Error; err != nil {
			t.Fatal(err)
		}
	}
	api := SessionAPI{DB: tx, ServiceToken: "session-test", MaxRows: 1}
	status, reply := callSessionEndpoint(t, api, http.MethodGet, ctx)
	if status != 200 || reply.Code != 0 || reply.Data == nil || reply.Data.Count != 1 || len(reply.Data.Items) != 1 || reply.Data.Items[0].SessionID != oldestSession || reply.Data.Items[0].RemoteAddr != "" || reply.Data.IdleThresholdSec != 900 {
		t.Fatalf("ordered bounded list/NULL address: status=%d reply=%+v", status, reply)
	}
	api.MaxRows = 2
	status, reply = callSessionEndpoint(t, api, http.MethodGet, ctx)
	if status != 200 || reply.Data == nil || reply.Data.Count != 2 || len(reply.Data.Items) != 2 || reply.Data.Items[1].SessionID != oldSession {
		t.Fatalf("eligible list: status=%d reply=%+v", status, reply)
	}
	for _, want := range []int64{2, 0} {
		status, reply = callSessionEndpoint(t, api, http.MethodPost, ctx)
		if status != 200 || reply.Code != 0 || reply.Data == nil || reply.Data.Closed != want || reply.Data.IdleThresholdSec != 900 {
			t.Fatalf("cleanup/replay: status=%d reply=%+v data=%+v want=%d", status, reply, reply.Data, want)
		}
	}
	for _, check := range []struct {
		where string // 需要保持的全部主键字段／关闭事实。
		args  []any  // 夹具身份及日期。
		want  int64  // 预期匹配行数。
	}{
		{"id = ? AND created_month = ? AND ended_at IS NULL", []any{id, month}, 1},
		{"id = ? AND created_month = ? AND close_reason = 'normal'", []any{id + 2, month}, 1},
		{"device_id = ? AND ended_at IS NOT NULL AND close_reason = 'abandoned'", []any{device}, 2},
	} {
		var count int64
		if err := tx.Table("device_session").Where(check.where, check.args...).Count(&count).Error; err != nil || count != check.want {
			t.Fatalf("composite identity/normal close changed: count=%d want=%d err=%v", count, check.want, err)
		}
	}
	status, reply = callSessionEndpoint(t, api, http.MethodGet, ctx)
	if status != 200 || reply.Data == nil || reply.Data.Items == nil || len(reply.Data.Items) != 0 || reply.Data.Count != 0 {
		t.Fatalf("empty list changed: status=%d reply=%+v", status, reply)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("device_session").Where("device_id = ?", device).Count(&existing).Error; err != nil || existing != 0 {
		t.Fatalf("fixture remains: %d err=%v", existing, err)
	}
}

// TestIdleSessionCleanupRollsBackCanceledBatch 在首行完整关闭后取消请求，验证整批回滚与恢复重放，夹具仅按自己的复合主键清理。
func TestIdleSessionCleanupRollsBackCanceledBatch(t *testing.T) {
	db := gatewayContractDB(t)
	var existing int64
	if err := db.Table("device_session").Where("ended_at IS NULL AND last_active_at < ?", time.Now().UTC().Add(-15*time.Minute)).Count(&existing).Error; err != nil || existing != 0 {
		t.Fatalf("non-fixture idle sessions: %d err=%v", existing, err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	id, device := testfixture.NewID(), "SESSION-CANCEL-"+uuid.NewString()
	t.Cleanup(func() {
		if err := db.Table("device_session").Where("id IN ? AND created_month = ?", []uint64{id, id + 1}, month).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
			return
		}
		if err := db.Table("device_session").Where("device_id = ?", device).Count(&existing).Error; err != nil || existing != 0 {
			t.Errorf("fixture remains: %d err=%v", existing, err)
		}
	})
	for offset := range 2 {
		if err := db.Table("device_session").Create(map[string]any{"id": id + uint64(offset), "created_month": month, "session_id": uuid.NewString(), "device_id": device, "protocol": "tcp", "started_at": month, "last_active_at": now.Add(-time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook, writes := "test:session-cancel-"+uuid.NewString(), 0
	if err := db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register(hook, func(update *gorm.DB) {
		if update.Statement.Table == "device_session" && update.Error == nil && update.RowsAffected == 1 {
			writes++
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Update().Remove(hook)
	api := SessionAPI{DB: db, ServiceToken: "session-test", MaxRows: 2}
	status, reply := callSessionEndpoint(t, api, http.MethodPost, ctx)
	if status != 503 || reply.Code != 5003 || reply.Message != "设备会话清理失败" || reply.Data != nil || writes != 1 {
		t.Fatalf("cancel after first row: status=%d reply=%+v writes=%d", status, reply, writes)
	}
	var closed int64
	if err := db.Table("device_session").Where("device_id = ? AND ended_at IS NOT NULL", device).Count(&closed).Error; err != nil || closed != 0 {
		t.Fatalf("canceled batch partially committed: closed=%d err=%v", closed, err)
	}
	if err := db.Callback().Update().Remove(hook); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{2, 0} {
		status, reply = callSessionEndpoint(t, api, http.MethodPost, context.Background())
		if status != 200 || reply.Code != 0 || reply.Data == nil || reply.Data.Closed != want {
			t.Fatalf("recovery/replay: status=%d reply=%+v want=%d", status, reply, want)
		}
	}
}
