//go:build integration

package pricing

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

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/google/uuid"
)

// pricingRouteSessionHook 仅提供测试会话记录；权限、账号及方案发布使用真实 MySQL。
// 此 fixture 不验证 Redis 生命周期或 Lua，会话真实实现的迁移验收属于 OP-01。
type pricingRouteSessionHook struct {
	records map[string]string // admin:session:<sid> 对应固定用户与权限版本 JSON
}

// DialHook 拒绝 next 对应的外部 Redis 连接；测试只允许 ProcessHook 提供的会话读。
func (pricingRouteSessionHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, errors.New("session fixture cannot dial")
	}
}

// ProcessHook 用 ctx 下 cmd 的键返回固定 EXISTS／GET；未声明命令报错，不调用 next 或真实 Redis。
func (h pricingRouteSessionHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
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
func (pricingRouteSessionHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error { return errors.New("unexpected session pipeline") }
}

// TestRegisteredSchemeRoutesPreserveAuthorizationAndResults 使用真实注册路由及账号权限验证完整方案流程。
// 会话存在性为 fixture，模板、范围、规则、凭据和审计使用 MySQL；全部造数在外层事务回滚。
func TestRegisteredSchemeRoutesPreserveAuthorizationAndResults(t *testing.T) {
	url := os.Getenv("TEST_MYSQL_URL")
	if url == "" {
		t.Fatal("TEST_MYSQL_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	tx := orm.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := uint64(time.Now().UnixNano())
	device := "HP" + strings.ReplaceAll(uuid.NewString(), "-", "")[:26]
	sid := uuid.NewString()
	var role uint64
	if err := tx.Table("role").Where("code='customer_admin' AND deleted_at IS NULL").Pluck("id", &role).Error; err != nil || role == 0 {
		t.Fatalf("role=%d %v", role, err)
	}
	for _, seed := range []struct {
		table string         /* 原身份及范围归属表 */
		row   map[string]any /* 唯一测试身份及必需字段 */
	}{
		{"admin_user", map[string]any{"id": id, "username": "pricing-route-" + sid, "password_hash": "fixture", "role_id": role, "status": "active", "auth_version": 1}},
		{"station", map[string]any{"id": id, "name": device, "longitude": 0, "latitude": 0, "status": "active"}},
		{"device_meta", map[string]any{"device_id": device, "station_id": id, "vendor_id": id, "protocol_adapter": "dc589", "status": "enabled"}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(err)
		}
	}
	jwt, err := auth.NewJWT("pricing-route-secret-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(id, 10), Kind: "admin", SessionID: sid, IssuedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	sessions := redis.NewClient(&redis.Options{Addr: "fixture-only"})
	defer sessions.Close()
	record, _ := json.Marshal(map[string]any{"uid": id, "version": 1})
	sessions.AddHook(pricingRouteSessionHook{records: map[string]string{"admin:session:" + sid: string(record)}})
	router := httpapi.NewRouter()
	API{Queries: pricing.QueryService{DB: tx}, Scopes: ops.ScopeQueryService{DB: tx}, Auth: authhttp.AdminAuth{Sessions: ops.SessionService{Accounts: ops.AccountRepo{DB: tx}, Sessions: ops.SessionRepo{Redis: sessions}, Tokens: ops.JWTAccess{JWT: jwt}}}, Schemes: pricing.SchemeService{DB: tx}, Publications: pricing.PublicationService{DB: tx}}.Register(router)
	base := "/api/v1/admin/settings/charging-schemes"
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
	scheme := charging.Scheme{Name: "真实路由套餐", Packages: []charging.Package{{ID: 1, Name: "一小时", Mode: "duration", PriceCents: 400, Minutes: 60}}}
	input, _ := json.Marshal(pricing.SchemeInput{Scheme: scheme})
	body := string(input)
	if w := call("POST", base, body, ""); w.Code != 401 {
		t.Fatalf("anonymous=%d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{`{"scheme":{}}`, body + " {}", `{"scheme":{},"unknown":1}`} {
		if w := call("POST", base, bad, token); w.Code != 400 {
			t.Fatalf("invalid=%d %s", w.Code, w.Body.String())
		}
	}
	w := call("POST", base, body, token)
	if w.Code != 200 {
		t.Fatalf("create=%d %s", w.Code, w.Body.String())
	}
	var created struct {
		Data pricing.SchemeResult `json:"data"` /* 原 HTTP 保存结果 */
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Data.ID == 0 || created.Data.Version != 1 {
		t.Fatalf("create result=%+v %v", created, err)
	}
	publication := pricing.PublishInput{RequestID: uuid.NewString(), StationID: id, DeviceID: device, TemplateID: created.Data.ID, TemplateVersion: 1}
	payload, _ := json.Marshal(publication)
	for i := 0; i < 2; i++ {
		w = call("POST", base+"/apply", string(payload), token)
		var out struct {
			Data pricing.PublicationResult `json:"data"` /* 原发布结果和重放标志 */
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Data.ID == 0 || out.Data.Version != 1 || out.Data.Replayed != (i == 1) {
			t.Fatalf("publish=%d %s %v", w.Code, w.Body.String(), err)
		}
	}
	publication.RequestID = uuid.NewString()
	publication.DeviceID = ""
	payload, _ = json.Marshal(publication)
	if err := tx.Table("admin_data_scope").Create(map[string]any{"admin_user_id": id, "scope_type": "vendor", "scope_id": id, "created_by": id}).Error; err != nil {
		t.Fatal(err)
	}
	if w := call("POST", base+"/apply", string(payload), token); w.Code != 403 {
		t.Fatalf("vendor default=%d %s", w.Code, w.Body.String())
	}
	if err := tx.Table("admin_data_scope").Where("admin_user_id=?", id).Delete(nil).Error; err != nil {
		t.Fatal(err)
	}
	if w := call("POST", base+"/apply", string(payload), token); w.Code != 200 {
		t.Fatalf("station default=%d %s", w.Code, w.Body.String())
	}
	inherit, _ := json.Marshal(pricing.InheritInput{DeviceID: device, ExpectedVersion: 1})
	path := "/api/v1/admin/stations/" + strconv.FormatUint(id, 10) + "/charging-scheme"
	if w := call("POST", path+"/inherit", string(inherit), token); w.Code != 200 || !strings.Contains(w.Body.String(), `"inherited":true`) {
		t.Fatalf("inherit=%d %s", w.Code, w.Body.String())
	}
	if w := call("GET", path+"?device_id="+device, "", token); w.Code != 200 || !strings.Contains(w.Body.String(), `"inherited":true`) {
		t.Fatalf("effective=%d %s", w.Code, w.Body.String())
	}

	if w := call("GET", base, "", token); w.Code != 200 || !strings.Contains(w.Body.String(), "真实路由套餐") {
		t.Fatalf("template list=%d %s", w.Code, w.Body)
	}
	capabilities := "/api/v1/admin/settings/device-capabilities?station_id=" + strconv.FormatUint(id, 10) + "&device_id=" + device
	if w := call("GET", capabilities, "", token); w.Code != 200 || !strings.Contains(w.Body.String(), "dc589") || !strings.Contains(w.Body.String(), "\"reports_energy\":true") {
		t.Fatalf("capabilities=%d %s", w.Code, w.Body)
	}
	if err := tx.Table("station").Create(map[string]any{"id": id + 2, "name": "未发布站点", "longitude": 0, "latitude": 0, "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/api/v1/admin/stations/"+strconv.FormatUint(id+2, 10)+"/charging-scheme", "", token); w.Code != 200 || !strings.Contains(w.Body.String(), "\"scheme\":null") || strings.Contains(w.Body.String(), "permissions") || strings.Contains(w.Body.String(), "rule_id") {
		t.Fatalf("empty scheme shape=%d %s", w.Code, w.Body)
	}
	if w := call("PUT", base+"/"+strconv.FormatUint(created.Data.ID, 10), body, token); w.Code != 409 {
		t.Fatalf("old version=%d %s", w.Code, w.Body.String())
	}
	if err := tx.Table("admin_user").Where("id=?", id).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if w := call("POST", base, body, token); w.Code != 401 {
		t.Fatalf("disabled actor=%d %s", w.Code, w.Body.String())
	}
}
