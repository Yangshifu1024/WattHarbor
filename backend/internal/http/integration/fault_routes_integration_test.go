//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"chargepilot/internal/device"
	"chargepilot/internal/domain/operations"
	adminorder "chargepilot/internal/http/admin/order"
	authhttp "chargepilot/internal/http/auth"
	userdevice "chargepilot/internal/http/user/device"
	"chargepilot/internal/identity"
	"chargepilot/internal/ops"
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/sessiontoken"
	"chargepilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestRegisteredFaultRoutesPreserveScopeVisibilityAndFailures 验证真实注册用户报修和后台指派／处理／历史。
// 用户、权限、审计及 Redis 为真实实现，DB 造数回滚并只删除本测试会话；未知设备与范围查询都使用注册入口。
func TestRegisteredFaultRoutesPreserveScopeVisibilityAndFailures(t *testing.T) {
	tx := cardRouteDB(t, "TEST_MYSQL_URL").Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	cache := identityRouteRedis(t)
	id := testfixture.NewID()
	tag := uuid.NewString()
	for _, seed := range []struct {
		table string
		row   map[string]any
	}{
		{"user", map[string]any{"id": id, "openid": tag, "status": "active"}},
		{"user", map[string]any{"id": id + 1, "openid": tag + "-other", "status": "active"}},
		{"role", map[string]any{"id": id + 2, "code": "feedback-" + tag, "name": "feedback proof", "is_builtin": false}},
		{"admin_user", map[string]any{"id": id + 3, "username": "feedback-" + tag, "password_hash": "fixture", "role_id": id + 2, "status": "active", "auth_version": 1}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatalf("seed %s %v", seed.table, err)
		}
	}
	for _, code := range []string{"feedback.read", "feedback.reply", "fault.read", "fault.dispatch", "fault.resolve"} {
		var p uint64
		if err := tx.Table("permission").Where("code=?", code).Pluck("id", &p).Error; err != nil || p == 0 {
			t.Fatalf("permission %s %v", code, err)
		}
		if err := tx.Table("role_permission").Create(map[string]any{"role_id": id + 2, "permission_id": p}).Error; err != nil {
			t.Fatal(err)
		}
	}
	jwt, err := auth.NewJWT("registered-feedback-secret-at-least-32")
	if err != nil {
		t.Fatal(err)
	}
	users := identity.UserRepo{DB: tx}
	sessions := identity.SessionRepo{Redis: cache}
	userLayout := sessiontoken.Layout{KeyPrefix: "user:session:"}
	userToken := func(uid uint64, openid string) string {
		sid, _, err := sessions.Create(context.Background(), identity.User{ID: uid, OpenID: openid})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := cache.Del(context.Background(), userLayout.Key(sid)).Err(); err != nil {
				t.Error(err)
			}
		})
		token, err := (identity.JWTAccess{JWT: jwt}).Sign(identity.User{ID: uid, OpenID: openid}, sid, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	owner, other := userToken(id, tag), userToken(id+1, tag+"-other")
	adminSessions := ops.SessionRepo{Redis: cache}
	adminSID, _, err := adminSessions.Create(context.Background(), operations.AccountIdentity{ID: id + 3, AuthVersion: 1, Username: "feedback-" + tag})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := adminSessions.RevokeSession(context.Background(), adminSID); err != nil {
			t.Error(err)
		}
	})
	adminToken, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(id+3, 10), Kind: "admin", SessionID: adminSID, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	service := device.FaultService{DB: tx}
	router := httpapi.NewRouter()
	userdevice.API{Faults: service, Auth: authhttp.SessionAuthenticator{Auth: authhttp.NewAuthenticator(jwt, sessions, users)}}.Register(router)
	(adminorder.API{Faults: service, Auth: authhttp.AdminAuth{Sessions: ops.SessionService{Accounts: ops.AccountRepo{DB: tx}, Sessions: adminSessions, Tokens: ops.JWTAccess{JWT: jwt}}}}).Register(router)
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	submitPath := "/api/v1/user/device/report-fault"
	if w := call("POST", submitPath, `{"device_id":"unknown","fault_type":"bad"}`, owner); w.Code != 400 {
		t.Fatalf("input=%d %s", w.Code, w.Body)
	}
	w := call("POST", submitPath, `{"device_id":"RF`+tag+`","port_no":9,"fault_type":"other","description":null,"images":null}`, owner)
	var submitted struct {
		Data struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &submitted); err != nil || w.Code != 200 || submitted.Data.Status != "open" {
		t.Fatalf("submit=%d %s %v", w.Code, w.Body, err)
	}
	report, err := strconv.ParseUint(submitted.Data.ID, 10, 64)
	if err != nil || report == 0 || strconv.FormatUint(report, 10) != submitted.Data.ID {
		t.Fatalf("invalid report ID %q: %v", submitted.Data.ID, err)
	}
	userPath := "/api/v1/user/device/fault-reports/" + submitted.Data.ID + "/history"
	faultPath := "/api/v1/admin/device-fault-reports/" + submitted.Data.ID
	if w := call("GET", userPath, "", other); w.Code != 404 {
		t.Fatalf("other=%d", w.Code)
	}
	if err := tx.Table("device_fault_report_event").Create(map[string]any{"report_id": report, "event_type": "migration_baseline", "note": "private fixture", "user_visible": false}).Error; err != nil {
		t.Fatal(err)
	}
	if w := call("GET", userPath, "", owner); w.Code != 200 || strings.Contains(w.Body.String(), "private fixture") {
		t.Fatalf("private=%d %s", w.Code, w.Body)
	}
	if w := call("GET", "/api/v1/user/device/fault-reports", "", owner); w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"`+submitted.Data.ID+`"`) || !strings.Contains(w.Body.String(), `"description":null`) || !strings.Contains(w.Body.String(), `"resolved_at":null`) {
		t.Fatalf("user shape=%d %s", w.Code, w.Body)
	}
	dispatch := fmt.Sprintf(`{"assigned_to":%d,"note":"registered dispatch"}`, id+3)
	for range 2 {
		if w := call("POST", faultPath+"/dispatch", dispatch, adminToken); w.Code != 200 {
			t.Fatalf("dispatch=%d %s", w.Code, w.Body)
		}
	}
	if w := call("POST", faultPath+"/resolve", `{"status":"closed"}`, adminToken); w.Code != 409 {
		t.Fatalf("skip=%d %s", w.Code, w.Body)
	}
	failure := errors.New("fault audit unavailable")
	key := "registered-fault-audit"
	if err := tx.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "audit_log" {
			q.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	w = call("POST", faultPath+"/resolve", `{"status":"fixed","note":"registered repair"}`, adminToken)
	tx.Callback().Create().Remove(key)
	if w.Code != 503 {
		t.Fatalf("audit=%d %s", w.Code, w.Body)
	}
	var status string
	if err := tx.Table("device_fault_report").Where("id=?", report).Pluck("status", &status).Error; err != nil || status != "dispatched" {
		t.Fatalf("partial=%s %v", status, err)
	}
	for _, body := range []string{`{"status":"fixed","note":"registered repair"}`, `{"status":"fixed","note":"replay"}`, `{"status":"closed"}`, `{"status":"closed"}`} {
		if w := call("POST", faultPath+"/resolve", body, adminToken); w.Code != 200 {
			t.Fatalf("resolve=%d %s", w.Code, w.Body)
		}
	}
	if w := call("GET", faultPath+"/history", "", adminToken); w.Code != 200 || !strings.Contains(w.Body.String(), `"total":5`) || !strings.Contains(w.Body.String(), "private fixture") {
		t.Fatalf("admin history=%d %s", w.Code, w.Body)
	}
	if w := call("GET", userPath, "", owner); w.Code != 200 || strings.Contains(w.Body.String(), "private fixture") {
		t.Fatalf("user history=%d %s", w.Code, w.Body)
	}
	// 用户归属查询失效必须是 503，不得隐藏成 404。
	if err := tx.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "device_fault_report" {
			q.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	w = call("GET", userPath, "", owner)
	tx.Callback().Query().Remove(key)
	if w.Code != 503 {
		t.Fatalf("owner storage=%d %s", w.Code, w.Body)
	}
	// 未知设备仅不受限管理员可见；有范围后列表／历史／处理均隐藏该报告。
	if err := tx.Table("admin_data_scope").Create(map[string]any{"admin_user_id": id + 3, "scope_type": "station", "scope_id": id + 4, "created_by": id + 3}).Error; err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/api/v1/admin/device-fault-reports", "", adminToken); w.Code != 200 || !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatalf("scope list=%d %s", w.Code, w.Body)
	}
	if w := call("GET", faultPath+"/history", "", adminToken); w.Code != 404 {
		t.Fatalf("scope history=%d %s", w.Code, w.Body)
	}
	if w := call("POST", faultPath+"/resolve", `{"status":"closed"}`, adminToken); w.Code != 404 {
		t.Fatalf("scope update=%d %s", w.Code, w.Body)
	}
	if err := tx.Table("device_meta").Create(map[string]any{"device_id": "RF" + tag, "station_id": id + 4, "status": "enabled", "protocol_adapter": "dc589"}).Error; err != nil {
		t.Fatal(err)
	}
	if w := call("GET", faultPath+"/history", "", adminToken); w.Code != 200 {
		t.Fatalf("scope present=%d %s", w.Code, w.Body)
	}
	if err := tx.Table("role_permission").Where("role_id=?", id+2).Delete(nil).Error; err != nil {
		t.Fatal(err)
	}
	if w := call("GET", faultPath+"/history", "", adminToken); w.Code != 403 {
		t.Fatalf("permission revoked=%d %s", w.Code, w.Body)
	}
}
