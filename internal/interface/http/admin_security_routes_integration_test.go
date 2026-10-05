//go:build integration

package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// TestRegisteredAdminSecurityLifecycle 使用真实MySQL／Redis和注册认证路由验证登记、确认、挑战和关闭。
// 账号、权限、状态与审计使用外层回滚；随机SID及本测试绑定／租锁／频次键逐项清理，不改bootstrap。
func TestRegisteredAdminSecurityLifecycle(t *testing.T) {
	tx := cardRouteDB(t, "TEST_MYSQL_URL").Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	cache := identityRouteRedis(t)
	ctx := context.Background()
	id := uint64(time.Now().UnixNano())
	tag := uuid.NewString()
	password := "Test!RegisteredSecurity2026"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct {
		table string
		row   map[string]any
	}{
		{"role", map[string]any{"id": id + 1, "code": "registered-security-" + tag, "name": "security fixture", "is_builtin": false}},
		{"admin_user", map[string]any{"id": id, "username": "security-" + tag, "password_hash": string(hash), "role_id": id + 1, "status": "active", "auth_version": 9007199254741111}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(err)
		}
	}
	jwt, err := auth.NewJWT(strings.Repeat("registered-security-", 3))
	if err != nil {
		t.Fatal(err)
	}
	repo := ops.SessionRepo{Redis: cache}
	service := ops.SessionService{Accounts: ops.AdminStore{DB: tx}, Sessions: repo, Tokens: ops.JWTAccess{JWT: jwt}, Limits: ops.AuthRateRepo{Redis: cache}}
	router := httpapi.NewRouter()
	(chargehttp.AdminAuth{Sessions: service, MFA: ops.MFAService{DB: tx, Repo: ops.MFARepo{Redis: cache}}}).Register(router)
	prefix := fmt.Sprintf("admin:mfa-settings:")
	for _, kind := range []string{"pending:", "lock:", "rate:"} {
		key := prefix + kind + strconv.FormatUint(id, 10)
		t.Cleanup(func() { cache.Del(ctx, key) })
	}
	counter := 0
	ip := func() string {
		counter++
		return fmt.Sprintf("2001:db8:%x:%x::%x", uint16(id>>16), uint16(id), counter)
	}
	call := func(method, path string, body any, token, source string) *httptest.ResponseRecorder {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if source == "" {
			source = ip()
		}
		req.RemoteAddr = net.JoinHostPort(source, "12345")
		t.Cleanup(func() { cache.Del(ctx, "admin:login-rate:"+source) })
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var envelope struct {
			Data ops.AccessReply `json:"data"`
		}
		if w.Code == 200 && json.Unmarshal(w.Body.Bytes(), &envelope) == nil && envelope.Data.AccessToken != "" {
			_, sid, err := (ops.JWTAccess{JWT: jwt}).Verify(envelope.Data.AccessToken)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := repo.RevokeSession(ctx, sid); err != nil {
					t.Error(err)
				}
			})
		}
		return w
	}
	data := func(w *httptest.ResponseRecorder, out any) {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("route status=%d body=%s", w.Code, w.Body)
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			t.Fatal(err)
		}
	}
	login := func() ops.AccessReply {
		var value ops.AccessReply
		data(call("POST", "/api/v1/admin/auth/login", map[string]any{"username": "security-" + tag, "password": password}, "", ""), &value)
		return value
	}
	first := login()
	second := login()
	settings := "/api/v1/admin/auth/mfa-settings"
	var enrolled ops.MFASettingsReply
	data(call("POST", settings, map[string]any{"action": "enrol", "password": password}, first.AccessToken, ""), &enrolled)
	if enrolled.Secret == "" || len(enrolled.EnrollmentID) != 43 || enrolled.ExpiresIn != 300 || !strings.HasPrefix(enrolled.OTPAuthURI, "otpauth://") || enrolled.MFAEnabled || enrolled.SessionsRevoked {
		t.Fatal("enrollment contract changed")
	}
	code, err := auth.TOTPCode(enrolled.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	confirm := map[string]any{"action": "confirm", "enrollment_id": enrolled.EnrollmentID, "code": code}
	if w := call("POST", settings, confirm, second.AccessToken, ""); w.Code != 409 {
		t.Fatalf("cross session=%d %s", w.Code, w.Body)
	}
	bad := map[string]any{"action": "confirm", "enrollment_id": enrolled.EnrollmentID, "code": "abcdef"}
	if w := call("POST", settings, bad, first.AccessToken, ""); w.Code != 400 {
		t.Fatalf("invalid format=%d", w.Code)
	}
	badCode := ""
	for candidate := 0; candidate < 10 && badCode == ""; candidate++ {
		code := fmt.Sprintf("%06d", candidate)
		if auth.VerifyTOTP(enrolled.Secret, code, time.Now()) != nil {
			badCode = code
		}
	}
	bad["code"] = badCode
	if w := call("POST", settings, bad, first.AccessToken, ""); w.Code != 400 {
		t.Fatalf("bad proof=%d", w.Code)
	}
	pendingKey := "admin:mfa-settings:pending:" + strconv.FormatUint(id, 10)
	if n, err := cache.Exists(ctx, pendingKey).Result(); err != nil || n != 1 {
		t.Fatalf("bad proof consumed %d %v", n, err)
	}
	var enabled ops.MFASettingsReply
	data(call("POST", settings, confirm, first.AccessToken, ""), &enabled)
	if !enabled.MFAEnabled || !enabled.SessionsRevoked || enabled.Secret != "" {
		t.Fatal("confirmation contract changed")
	}
	if w := call("GET", "/api/v1/admin/auth/me", nil, first.AccessToken, ""); w.Code != 401 {
		t.Fatalf("enable revocation=%d", w.Code)
	}
	var challenge struct {
		Required  bool   `json:"mfa_required"`
		Challenge string `json:"mfa_challenge"`
		Token     string `json:"token"`
	}
	data(call("POST", "/api/v1/admin/auth/login", map[string]any{"username": "security-" + tag, "password": password}, "", ""), &challenge)
	if !challenge.Required || challenge.Challenge == "" || challenge.Token != "" {
		t.Fatal("MFA challenge contract changed")
	}
	t.Cleanup(func() { cache.Del(ctx, "admin:mfa:"+challenge.Challenge) })
	code, err = auth.TOTPCode(enrolled.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	mfaBody := map[string]any{"mfa_challenge": challenge.Challenge, "code": code}
	var verified ops.AccessReply
	data(call("POST", "/api/v1/admin/auth/mfa", mfaBody, "", ""), &verified)
	if !verified.MFAEnabled || verified.Token == "" || verified.Token != verified.AccessToken || verified.ExpiresIn != 28800 {
		t.Fatal("MFA issue contract changed")
	}
	if w := call("POST", "/api/v1/admin/auth/mfa", mfaBody, "", ""); w.Code != 401 {
		t.Fatalf("challenge replay=%d", w.Code)
	}
	var disabled ops.MFASettingsReply
	data(call("POST", settings, map[string]any{"action": "disable", "password": password, "code": code}, verified.AccessToken, ""), &disabled)
	if disabled.MFAEnabled || !disabled.SessionsRevoked {
		t.Fatal("disable contract changed")
	}
	if w := call("GET", "/api/v1/admin/auth/me", nil, verified.AccessToken, ""); w.Code != 401 {
		t.Fatalf("disable revocation=%d", w.Code)
	}
	final := login()
	// 故障只注入本测试连接的审计创建，确认后的SQL启用和版本必须整体回滚；缓存消费不伪装回滚。
	rateKey := "admin:mfa-settings:rate:" + strconv.FormatUint(id, 10)
	if err := cache.Del(ctx, rateKey).Err(); err != nil {
		t.Fatal(err)
	}
	data(call("POST", settings, map[string]any{"action": "enrol", "password": password}, final.AccessToken, ""), &enrolled)
	code, err = auth.TOTPCode(enrolled.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fixtureErr := errors.New("security audit fixture")
	name := "security-audit-" + tag
	if err := tx.Callback().Create().Before("gorm:create").Register(name, func(db *gorm.DB) {
		if db.Statement.Table == "audit_log" {
			db.AddError(fixtureErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	w := call("POST", settings, map[string]any{"action": "confirm", "enrollment_id": enrolled.EnrollmentID, "code": code}, final.AccessToken, "")
	tx.Callback().Create().Remove(name)
	if w.Code != 503 {
		t.Fatalf("audit failure=%d %s", w.Code, w.Body)
	}
	var actual ops.AdminAccount
	if err := tx.Where("id=?", id).Take(&actual).Error; err != nil || actual.MFAEnabled {
		t.Fatalf("MFA rollback enabled=%v err=%v", actual.MFAEnabled, err)
	}
	if w := call("GET", "/api/v1/admin/auth/me", nil, final.AccessToken, ""); w.Code != 200 {
		t.Fatalf("rollback version=%d", w.Code)
	}
	if n, err := cache.Exists(ctx, pendingKey).Result(); err != nil || n != 0 {
		t.Fatalf("consumed cache restored %d %v", n, err)
	}
	// 同一真实来源的坏输入也计数，第六次返回原429与Retry-After，不接触账号锁。
	source := ip()
	for i := range 6 {
		w := call("POST", "/api/v1/admin/auth/login", map[string]any{}, "", source)
		want := 400
		if i == 5 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("rate %d status=%d", i, w.Code)
		}
		if i == 5 && w.Header().Get("Retry-After") != "60" {
			t.Fatal("retry header changed")
		}
	}
	var audits []map[string]any
	if err := tx.Table("audit_log").Where("actor_id=?", id).Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(audits)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{password, enrolled.Secret, enrolled.EnrollmentID, code, challenge.Challenge, first.AccessToken, first.RefreshToken} {
		if secret != "" && strings.Contains(string(raw), secret) {
			t.Fatal("sensitive proof reached audit")
		}
	}
}
