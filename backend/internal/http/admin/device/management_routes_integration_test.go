//go:build integration

package device

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/redis/go-redis/v9"
	"net"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	business "github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/control"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/provision"
	stationhttp "github.com/ChargePilot2026/charge-pilot/internal/http/admin/station"
	vendorhttp "github.com/ChargePilot2026/charge-pilot/internal/http/admin/vendor"
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/testfixture"
	"github.com/google/uuid"

	"gorm.io/gorm"
)

// deviceRouteSessionHook 仅提供测试会话记录；权限、账号及站点设备资料使用真实 MySQL。
// 此 fixture 不验证 Redis 生命周期或 Lua，会话真实实现的迁移验收属于 OP-01。
type deviceRouteSessionHook struct {
	records map[string]string // admin:session:<sid> 对应固定用户与权限版本 JSON
}

// DialHook 拒绝 next 对应的外部 Redis 连接；测试只允许 ProcessHook 提供的会话读。
func (deviceRouteSessionHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, errors.New("session fixture cannot dial")
	}
}

// ProcessHook 用 ctx 下 cmd 的键返回固定 EXISTS／GET；未声明命令报错，不调用 next 或真实 Redis。
func (h deviceRouteSessionHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		key, ok := cmd.Args()[1].(string)
		if !ok {
			return errors.New("invalid fixture key")
		}
		record, exists := h.records[key]
		switch v := cmd.(type) {
		case *redis.IntCmd:
			if v.Name() != "exists" {
				return errors.New("unexpected int command")
			}
			if exists {
				v.SetVal(1)
			} else {
				v.SetVal(0)
			}
			return nil
		case *redis.StringCmd:
			if v.Name() != "get" {
				return errors.New("unexpected string command")
			}
			if !exists {
				v.SetErr(redis.Nil)
				return redis.Nil
			}
			v.SetVal(record)
			return nil
		default:
			return errors.New("unexpected session command")
		}
	}
}

// ProcessPipelineHook 拒绝 ctx 下 cmds 批量读写；fixture 不声明流水线能力，不调用 next。
func (deviceRouteSessionHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error { return errors.New("unexpected session pipeline") }
}

// managementRouteDB 打开 env 指定的独立真库连接，由 t 关闭；缺失、连接失败立即终止，不跳过。
func managementRouteDB(t *testing.T, env string) *gorm.DB {
	t.Helper()
	uri := os.Getenv(env)
	if uri == "" {
		t.Fatalf("%s is required", env)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := dbconn.Open(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}

// TestRegisteredDeviceAndStationManagementUsesCurrentScope 验证实际注册后台及网关摘要端点。
// 两库造数在各自外层事务回滚；会话存在性为 fixture，权限、范围、版本、审计和设备摘要均用真实 MySQL。
func TestRegisteredDeviceAndStationManagementUsesCurrentScope(t *testing.T) {
	central := managementRouteDB(t, "TEST_MYSQL_URL").Begin()
	defer central.Rollback()
	gateway := managementRouteDB(t, "DATABASE_URL_GATEWAY").Begin()
	defer gateway.Rollback()
	if central.Error != nil || gateway.Error != nil {
		t.Fatalf("begin: %v %v", central.Error, gateway.Error)
	}
	id, sid := testfixture.NewID(), uuid.NewString()
	visible := "MV" + strings.ReplaceAll(uuid.NewString(), "-", "")[:26]
	hidden := "MH" + strings.ReplaceAll(uuid.NewString(), "-", "")[:26]
	at := time.Date(2026, 10, 1, 0, 0, 0, 123000000, time.UTC)
	var role uint64
	if err := central.Table("role").Where("code='customer_admin' AND deleted_at IS NULL").Pluck("id", &role).Error; err != nil || role == 0 {
		t.Fatalf("role=%d %v", role, err)
	}
	for _, seed := range []struct {
		db    *gorm.DB       // 目标真库
		table string         // 所属表
		row   map[string]any // 唯一测试身份及必需列
	}{
		{central, "admin_user", map[string]any{"id": id, "username": "device-route-" + sid, "password_hash": "fixture", "role_id": role, "status": "active", "auth_version": 1}},
		{central, "station", map[string]any{"id": id, "name": visible, "longitude": 0, "latitude": 0, "status": "active"}},
		{central, "station", map[string]any{"id": id + 1, "name": hidden, "longitude": 0, "latitude": 0, "status": "active"}},
		{central, "admin_data_scope", map[string]any{"admin_user_id": id, "scope_type": "station", "scope_id": id, "created_by": id}},
		{central, "admin_data_scope", map[string]any{"admin_user_id": id, "scope_type": "vendor", "scope_id": id, "created_by": id}},
		{central, "device_meta", map[string]any{"device_id": visible, "station_id": id, "vendor_id": id, "protocol_adapter": "dc589", "status": "enabled", "updated_at": at}},
		{central, "device_meta", map[string]any{"device_id": hidden, "station_id": id + 1, "vendor_id": id + 1, "protocol_adapter": "dc589", "status": "enabled", "updated_at": at}},
		{gateway, "vendor", map[string]any{"id": id, "vendor_code": visible, "vendor_name": "真实摘要厂商", "adapter_class": "dc589", "protocol": "tcp", "status": "enabled"}},
		{gateway, "device", map[string]any{"device_id": visible, "vendor_id": id, "station_id": id, "port_count": 2, "status": "enabled", "signal_strength": 0, "signal_at": at}},
		{gateway, "device_port", map[string]any{"device_id": visible, "port_no": 1, "port_code": visible + "-1", "reported_status": 0, "reported_status_at": at}},
		{gateway, "device_port", map[string]any{"device_id": visible, "port_no": 2, "port_code": visible + "-2"}},
	} {
		if err := seed.db.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatalf("seed %s: %v", seed.table, err)
		}
	}
	gwRouter := httpapi.NewRouter()
	(control.DeviceSummaryAPI{DB: gateway, ServiceToken: "actual-summary-service"}).Register(gwRouter)
	(provision.API{DB: gateway, ServiceToken: "actual-summary-service"}).Register(gwRouter)
	server := httptest.NewServer(gwRouter)
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{}, server.URL, "actual-summary-service")
	if err != nil {
		t.Fatal(err)
	}
	jwt, err := auth.NewJWT("device-route-secret-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(id, 10), Kind: "admin", SessionID: sid, IssuedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	cache := redis.NewClient(&redis.Options{Addr: "fixture-only"})
	defer cache.Close()
	record, _ := json.Marshal(map[string]any{"uid": id, "version": 1})
	cache.AddHook(deviceRouteSessionHook{records: map[string]string{"admin:session:" + sid: string(record)}})
	authAPI := authhttp.AdminAuth{Sessions: ops.SessionService{Accounts: ops.AccountRepo{DB: central}, Sessions: ops.SessionRepo{Redis: cache}, Tokens: ops.JWTAccess{JWT: jwt}}}
	router := httpapi.NewRouter()
	API{Scopes: ops.ScopeQueryService{DB: central}, Auth: authAPI, Management: business.ManagementService{DB: central, Summaries: client}, Imports: business.ImportService{DB: central, Gateway: client, Compatibility: pricing.Repo{DB: central}, LoadScope: func(ctx context.Context, p operations.Profile) (operations.DataScope, error) {
		return ops.LoadDataScope(ctx, central, p)
	}}}.Register(router)
	(vendorhttp.API{Scopes: ops.ScopeQueryService{DB: central}, Auth: authAPI, Vendors: business.VendorService{DB: central, Gateway: client}}).Register(router)
	(stationhttp.API{Scopes: ops.ScopeQueryService{DB: central}, Auth: authAPI, Stations: business.StationService{DB: central}}).Register(router)
	call := func(method, path, body, bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	base, stations := "/api/v1/admin/devices/", "/api/v1/admin/stations/"
	if w := call("GET", base+visible, "", ""); w.Code != 401 {
		t.Fatalf("anonymous=%d %s", w.Code, w.Body)
	}
	for _, path := range []string{base + hidden, "/api/v1/admin/devices?station_id=" + strconv.FormatUint(id+1, 10), stations + strconv.FormatUint(id+1, 10)} {
		want := 403
		if path == base+hidden {
			want = 404
		}
		if w := call("GET", path, "", token); w.Code != want {
			t.Fatalf("scope %s=%d %s", path, w.Code, w.Body)
		}
	}
	stationInput := `{"name":"修改可见站点","longitude":0,"latitude":0,"status":"active","address":null,"contact_phone":null}`
	if w := call("PUT", stations+strconv.FormatUint(id+1, 10), stationInput, token); w.Code != 403 {
		t.Fatalf("hidden station edit=%d %s", w.Code, w.Body)
	}
	var stationName string
	if err := central.Table("station").Where("id=?", id+1).Pluck("name", &stationName).Error; err != nil || stationName != hidden {
		t.Fatalf("hidden mutated=%s %v", stationName, err)
	}
	if w := call("PUT", stations+strconv.FormatUint(id, 10), stationInput, token); w.Code != 200 {
		t.Fatalf("station edit=%d %s", w.Code, w.Body)
	}
	if w := call("GET", stations+strconv.FormatUint(id, 10), "", token); w.Code != 200 || !strings.Contains(w.Body.String(), "修改可见站点") {
		t.Fatalf("station=%d %s", w.Code, w.Body)
	}
	w := call("GET", base+visible, "", token)
	var out struct {
		Data business.Device `json:"data"` /* 原设备响应 */
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("detail=%d %s %v", w.Code, w.Body, err)
	}
	if !out.Data.RuntimeAvailable || out.Data.VendorName == nil || *out.Data.VendorName != "真实摘要厂商" || out.Data.SignalStrength == nil || *out.Data.SignalStrength != 0 || len(out.Data.Ports) != 2 || out.Data.Ports[0].StatusCode == nil || *out.Data.Ports[0].StatusCode != 0 || out.Data.Ports[1].StatusCode != nil || out.Data.Ports[1].StatusAt != nil {
		t.Fatalf("summary changed nullable zero: %+v", out.Data)
	}
	payload := `{"model":"更新型号","serial_no":null,"warranty_until":null,"tags":["标签"],"expected_updated_at":"` + at.Format(time.RFC3339Nano) + `"}`
	if w := call("PUT", base+hidden, payload, token); w.Code != 404 {
		t.Fatalf("hidden device edit=%d %s", w.Code, w.Body)
	}
	w = call("PUT", base+visible, payload, token)
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Data.Model == nil || *out.Data.Model != "更新型号" || !out.Data.UpdatedAt.After(at) || !out.Data.RuntimeAvailable {
		t.Fatalf("update=%d %s %v", w.Code, w.Body, err)
	}
	if w := call("PUT", base+visible, payload, token); w.Code != 409 {
		t.Fatalf("stale update=%d %s", w.Code, w.Body)
	}
	w = call("PUT", base+visible+"/status", `{"status":"disabled"}`, token)
	if w.Code != 200 {
		t.Fatalf("status=%d %s", w.Code, w.Body)
	}
	var gatewayStatus string
	if err := gateway.Table("device").Where("device_id=?", visible).Pluck("status", &gatewayStatus).Error; err != nil || gatewayStatus != "enabled" {
		t.Fatalf("gateway operational coupling=%s %v", gatewayStatus, err)
	}
	fault, key := errors.New("injected management audit failure"), "management_audit_"+sid
	if err := central.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "audit_log" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	w = call("PUT", base+visible+"/status", `{"status":"enabled"}`, token)
	central.Callback().Create().Remove(key)
	if w.Code != 503 {
		t.Fatalf("audit failure=%d %s", w.Code, w.Body)
	}
	var stored string
	if err := central.Table("device_meta").Where("device_id=?", visible).Pluck("status", &stored).Error; err != nil || stored != "disabled" {
		t.Fatalf("audit failure half committed=%s %v", stored, err)
	}

	vendors := "/api/v1/admin/vendors"
	for _, path := range []string{vendors, "/api/v1/admin/vendor-options"} {
		w := call("GET", path, "", token)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "真实摘要厂商") {
			t.Fatalf("vendor list=%d %s", w.Code, w.Body)
		}
	}
	if w := call("GET", vendors+"/"+strconv.FormatUint(id+1, 10), "", token); w.Code != 404 {
		t.Fatalf("hidden vendor=%d %s", w.Code, w.Body)
	}
	if w := call("POST", vendors, "{}", token); w.Code != 403 {
		t.Fatalf("restricted vendor create=%d %s", w.Code, w.Body)
	}
	vendorInput := map[string]any{"vendor_code": visible, "vendor_name": "更新公开厂商", "adapter_class": "dc589", "protocol": "tcp", "status": "enabled"}
	vendorBody, _ := json.Marshal(vendorInput)
	if w := call("PUT", vendors+"/"+strconv.FormatUint(id, 10), string(vendorBody), token); w.Code != 200 || !strings.Contains(w.Body.String(), "更新公开厂商") {
		t.Fatalf("vendor update=%d %s", w.Code, w.Body)
	}
	vendorInput["vendor_code"] = "changed-code"
	vendorBody, _ = json.Marshal(vendorInput)
	if w := call("PUT", vendors+"/"+strconv.FormatUint(id, 10), string(vendorBody), token); w.Code != 409 {
		t.Fatalf("vendor code changed=%d %s", w.Code, w.Body)
	}
	importID := uuid.NewString()
	importBoard := "RI" + strings.ReplaceAll(uuid.NewString(), "-", "")[:26]
	importBody, _ := json.Marshal(business.ImportInput{ImportID: importID, Devices: []business.ImportDevice{{DeviceID: importBoard, VendorID: id, StationID: id, PortCount: 2}}})
	for i := 0; i < 2; i++ {
		w := call("POST", "/api/v1/admin/device-imports", string(importBody), token)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "completed") || !strings.Contains(w.Body.String(), importID) {
			t.Fatalf("registered import=%d %s", w.Code, w.Body)
		}
	}
	if w := call("POST", "/api/v1/admin/device-imports/"+importID+"/retry", "", token); w.Code != 200 {
		t.Fatalf("registered retry=%d %s", w.Code, w.Body)
	}
	for _, db := range []*gorm.DB{central, gateway} {
		table := "device_meta"
		if db == gateway {
			table = "device"
		}
		var n int64
		if err := db.Table(table).Where("device_id=?", importBoard).Count(&n).Error; err != nil || n != 1 {
			t.Fatalf("registered import %s=%d %v", table, n, err)
		}
	}
	var ports int64
	if err := gateway.Table("device_port").Where("device_id=?", importBoard).Count(&ports).Error; err != nil || ports != 2 {
		t.Fatalf("registered import ports=%d %v", ports, err)
	}
	if err := central.Table("admin_data_scope").Where("admin_user_id=? AND scope_type='station'", id).Update("scope_id", id+1).Error; err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/api/v1/admin/device-imports/"+importID+"/retry", "", token); w.Code != 403 {
		t.Fatalf("retry revoked scope=%d %s", w.Code, w.Body)
	}

	// 厂商跨服务写入已提交后，中央审计失败沿用原尽力记录语义，不能伪报网关回滚。
	vendorInput["vendor_code"] = visible
	vendorInput["vendor_name"] = "网关已提交审计故障"
	vendorBody, _ = json.Marshal(vendorInput)
	if err := central.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "audit_log" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	w = call("PUT", vendors+"/"+strconv.FormatUint(id, 10), string(vendorBody), token)
	central.Callback().Create().Remove(key)
	if w.Code != 200 {
		t.Fatalf("remote committed audit=%d %s", w.Code, w.Body)
	}
	var committedVendorName string
	if err := gateway.Table("vendor").Where("id=?", id).Pluck("vendor_name", &committedVendorName).Error; err != nil || committedVendorName != "网关已提交审计故障" {
		t.Fatalf("remote commit=%s %v", committedVendorName, err)
	}
	// 没有厂商限制时仍由 vendor.create 授权创建；站点限制不等于厂商限制。
	if err := central.Table("admin_data_scope").Where("admin_user_id=? AND scope_type='vendor'", id).Delete(nil).Error; err != nil {
		t.Fatal(err)
	}
	vendorInput["vendor_code"] = "VC" + strings.ReplaceAll(uuid.NewString(), "-", "")[:26]
	vendorInput["vendor_name"] = "注册新厂商"
	vendorBody, _ = json.Marshal(vendorInput)
	if w := call("POST", vendors, string(vendorBody), token); w.Code != 200 || !strings.Contains(w.Body.String(), "注册新厂商") {
		t.Fatalf("vendor create=%d %s", w.Code, w.Body)
	}
	// 新站点没有已存在身份，沿用 station.create 授权，不自动给当前账号增加范围。
	if w := call("POST", "/api/v1/admin/stations", stationInput, token); w.Code != 200 {
		t.Fatalf("station create=%d %s", w.Code, w.Body)
	}
	var audits int64
	if err := central.Table("audit_log").Where("actor_id=?", id).Count(&audits).Error; err != nil || audits != 8 {
		t.Fatalf("audit count=%d %v", audits, err)
	}
	if err := central.Table("admin_user").Where("id=?", id).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if w := call("PUT", base+visible+"/status", `{"status":"enabled"}`, token); w.Code != 401 {
		t.Fatalf("revoked=%d %s", w.Code, w.Body)
	}
}
