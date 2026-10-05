//go:build integration

package http_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/control"
	centralclient "github.com/ChargePilot2026/charge-pilot/internal/integration/central"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	alertsync "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/alertsync"
	adminops "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/alerts"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TestRegisteredAlertOwnershipScopeAndWorkerTrigger 验证真实两库／Redis、后台列表认领及worker→central→gateway具名链路。
// 造数全部回滚，Redis只清理自身SID；设备事件为fixture，不代表实机告警。
func TestRegisteredAlertOwnershipScopeAndWorkerTrigger(t *testing.T) {
	tx, gw := cardRouteDB(t, "TEST_MYSQL_URL").Begin(), cardRouteDB(t, "DATABASE_URL_GATEWAY").Begin()
	defer tx.Rollback()
	defer gw.Rollback()
	if tx.Error != nil || gw.Error != nil {
		t.Fatal(tx.Error, gw.Error)
	}
	id, tag := uint64(time.Now().UnixNano()), uuid.NewString()
	board, outside, syncBoard := "AL"+tag, "AO"+tag, "SY"+tag
	randomID := uuid.New()
	alertID := binary.LittleEndian.Uint64(randomID[:8])%(1<<54) + (1 << 54)
	month := time.Now().UTC().Format("2006-01") + "-01"
	for _, seed := range []struct {
		table string
		row   map[string]any
	}{
		{"role", map[string]any{"id": id, "code": tag, "name": "alert proof", "is_builtin": false}},
		{"admin_user", map[string]any{"id": id, "username": tag, "password_hash": "fixture", "role_id": id, "status": "active", "auth_version": 1}},
		{"admin_data_scope", map[string]any{"admin_user_id": id, "scope_type": "station", "scope_id": id, "created_by": id}},
		{"admin_data_scope", map[string]any{"admin_user_id": id, "scope_type": "vendor", "scope_id": id, "created_by": id}},
		{"device_meta", map[string]any{"device_id": board, "station_id": id, "vendor_id": id, "status": "enabled", "protocol_adapter": "dc589"}},
		{"device_meta", map[string]any{"device_id": outside, "station_id": id + 1, "vendor_id": id, "status": "enabled", "protocol_adapter": "dc589"}},
		{"alert_event", map[string]any{"id": alertID, "device_id": board, "metric": "registered_fixture", "status": "active", "created_month": month}},
		{"alert_event", map[string]any{"id": alertID + 1, "device_id": outside, "metric": "registered_fixture", "status": "active", "created_month": month}},
		{"alert_event", map[string]any{"id": alertID + 2, "device_id": "unknown" + tag, "metric": "registered_fixture", "status": "active", "created_month": month}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(seed.table, err)
		}
	}
	for _, code := range []string{"alert.read", "alert.ack"} {
		var pid uint64
		if err := tx.Table("permission").Where("code=?", code).Pluck("id", &pid).Error; err != nil || pid == 0 {
			t.Fatal(code, err)
		}
		if err := tx.Table("role_permission").Create(map[string]any{"role_id": id, "permission_id": pid}).Error; err != nil {
			t.Fatal(err)
		}
	}
	jwt, err := auth.NewJWT("registered-alert-secret-at-least-32")
	if err != nil {
		t.Fatal(err)
	}
	cache := identityRouteRedis(t)
	sessions := ops.SessionRepo{Redis: cache}
	sid, _, err := sessions.Create(context.Background(), operations.AccountIdentity{ID: id, AuthVersion: 1, Username: tag})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sessions.RevokeSession(context.Background(), sid); err != nil {
			t.Error(err)
		}
	})
	token, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(id, 10), Kind: "admin", SessionID: sid, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter()
	(adminops.API{Alerts: device.AlertService{DB: tx}, Announcements: ops.AnnouncementService{DB: tx}, Audits: ops.AuditQueryService{DB: tx}, Auth: chargehttp.AdminAuth{Sessions: ops.SessionService{Accounts: ops.AccountRepo{DB: tx}, Sessions: sessions, Tokens: ops.JWTAccess{JWT: jwt}}}}).Register(router)
	call := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	w := call("GET", "/api/v1/admin/alerts")
	var page struct {
		Data struct {
			Items []json.RawMessage `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Data.Items) != 1 || !strings.Contains(w.Body.String(), `"id":`+strconv.FormatUint(alertID, 10)) {
		t.Fatalf("scope list=%d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/v1/admin/alerts/"+strconv.FormatUint(alertID+1, 10)+"/ack"); w.Code != 404 {
		t.Fatalf("outside=%d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/v1/admin/alerts/"+strconv.FormatUint(alertID+2, 10)+"/ack"); w.Code != 404 {
		t.Fatalf("unknown=%d %s", w.Code, w.Body)
	}
	fault := errors.New("alert audit failed")
	key := "registered_alert_audit"
	if err := tx.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "audit_log" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/alerts/" + strconv.FormatUint(alertID, 10) + "/ack"
	w = call("POST", path)
	tx.Callback().Create().Remove(key)
	if w.Code != 503 {
		t.Fatalf("audit=%d %s", w.Code, w.Body)
	}
	var row struct {
		Status  string
		AckedBy *uint64
	}
	if err := tx.Table("alert_event").Where("id=?", alertID).Take(&row).Error; err != nil || row.Status != "active" || row.AckedBy != nil {
		t.Fatalf("partial=%+v %v", row, err)
	}
	for range 2 {
		if w := call("POST", path); w.Code != 200 {
			t.Fatalf("ack=%d %s", w.Code, w.Body)
		}
	}
	var n int64
	if err := tx.Table("audit_log").Where("actor_id=? AND target_type='alert'", id).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("audits=%d %v", n, err)
	}
	if err := tx.Table("role_permission").Where("role_id=?", id).Delete(nil).Error; err != nil {
		t.Fatal(err)
	}
	if w := call("POST", path); w.Code != 403 {
		t.Fatalf("revoked=%d", w.Code)
	}
	// 内部服务链路采用真实注册端点；只限定本测试设备，避免共享库其他fixture进入恢复扫描。
	if err := tx.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "alert_event" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "device_id", Value: syncBoard}}})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer tx.Callback().Query().Remove(key)
	at := time.Now().UTC().Truncate(time.Millisecond)
	event := protocol.Event{Protocol: "dc589", DeviceID: syncBoard, Type: protocol.Fault, Port: 1, FaultCode: 3, ReceivedAt: at}
	payload, _ := json.Marshal(event)
	if err := gw.Table("device_event").Create(map[string]any{"id": alertID + 3, "event_key": uuid.NewString(), "device_id": syncBoard, "protocol_name": "dc589", "event_type": "fault", "port_no": 1, "received_at": at, "event_json": string(payload)}).Error; err != nil {
		t.Fatal(err)
	}
	gatewayRouter := httpapi.NewRouter()
	if err := gw.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "device_event" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "device_id", Value: syncBoard}}})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer gw.Callback().Query().Remove(key)
	(control.TelemetryAPI{DB: gw, ServiceToken: "registered-alert-token"}).Register(gatewayRouter)
	gatewayServer := httptest.NewServer(gatewayRouter)
	defer gatewayServer.Close()
	gateway, err := gatewayclient.NewClient(serviceclient.Client{HTTP: gatewayServer.Client()}, gatewayServer.URL, "registered-alert-token")
	if err != nil {
		t.Fatal(err)
	}
	centralRouter := httpapi.NewRouter()
	(alertsync.DeviceAlertSyncAPI{Sync: device.AlertSyncService{DB: tx, Gateway: gateway}, ServiceToken: "registered-alert-token"}).Register(centralRouter)
	centralServer := httptest.NewServer(centralRouter)
	defer centralServer.Close()
	central, err := centralclient.NewClient(serviceclient.Client{HTTP: centralServer.Client()}, centralServer.URL, "registered-alert-token")
	if err != nil {
		t.Fatal(err)
	}
	worker := alerts.Sync{Central: central}
	raised, err := worker.Run(context.Background())
	if err != nil || raised != 1 {
		t.Fatalf("trigger=%d %v", raised, err)
	}
	raised, err = worker.Run(context.Background())
	if err != nil || raised != 0 {
		t.Fatalf("trigger replay=%d %v", raised, err)
	}
}
