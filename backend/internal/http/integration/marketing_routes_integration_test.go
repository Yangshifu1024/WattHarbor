//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"chargepilot/internal/domain/operations"
	adminmarketing "chargepilot/internal/http/admin/marketing"
	authhttp "chargepilot/internal/http/auth"
	usermarketing "chargepilot/internal/http/user/marketing"
	"chargepilot/internal/identity"
	"chargepilot/internal/marketing"
	"chargepilot/internal/ops"
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/sessiontoken"
	"chargepilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestRegisteredMarketingKeepsIdentityAuditAndFailureKinds 使用真实 MySQL／Redis 与路由验证营销接口，造数回滚且只删除自身 SID。
func TestRegisteredMarketingKeepsIdentityAuditAndFailureKinds(t *testing.T) {
	tx := cardRouteDB(t, "TEST_MYSQL_URL").Begin()
	defer tx.Rollback()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	cache := identityRouteRedis(t)
	id := testfixture.NewID()
	tag := uuid.NewString()
	for _, seed := range []struct {
		// table 本断言写入、读取或清理的真实表名。
		table string
		// row 写入该表的隔离测试记录。
		row map[string]any
	}{
		{"user", map[string]any{"id": id, "openid": tag, "status": "active"}},
		{"role", map[string]any{"id": id + 1, "code": tag, "name": "marketing-proof", "is_builtin": false}},
		{"admin_user", map[string]any{"id": id + 2, "username": tag, "password_hash": "fixture", "role_id": id + 1, "status": "active", "auth_version": 1}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(seed.table, err)
		}
	}
	for _, code := range []string{"coupon.read", "coupon.create", "coupon.update", "coupon.grant", "coupon.activity.read", "coupon.activity.manage"} {
		var pid uint64
		if err := tx.Table("permission").Where("code=?", code).Pluck("id", &pid).Error; err != nil || pid == 0 {
			t.Fatal(code, err)
		}
		if err := tx.Table("role_permission").Create(map[string]any{"role_id": id + 1, "permission_id": pid}).Error; err != nil {
			t.Fatal(err)
		}
	}
	jwt, err := auth.NewJWT("marketing-route-secret-at-least-32")
	if err != nil {
		t.Fatal(err)
	}
	admins := ops.SessionRepo{Redis: cache}
	sid, _, err := admins.Create(context.Background(), operations.AccountIdentity{ID: id + 2, AuthVersion: 1, Username: tag})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admins.RevokeSession(context.Background(), sid); err != nil {
			t.Error(err)
		}
	})
	adminToken, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(id+2, 10), Kind: "admin", SessionID: sid, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	sessions := identity.SessionRepo{Redis: cache}
	user := identity.User{ID: id, OpenID: tag}
	userSID, _, err := sessions.Create(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cache.Del(context.Background(), (sessiontoken.Layout{KeyPrefix: "user:session:"}).Key(userSID)).Err(); err != nil {
			t.Error(err)
		}
	})
	userToken, err := (identity.JWTAccess{JWT: jwt}).Sign(user, userSID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter()
	userAuth := authhttp.SessionAuthenticator{Auth: authhttp.NewAuthenticator(jwt, sessions, identity.UserRepo{DB: tx})}
	(adminmarketing.API{Coupons: marketing.CouponService{DB: tx}, Activities: marketing.ActivityService{DB: tx}, Auth: authhttp.AdminAuth{Sessions: ops.SessionService{Accounts: ops.AccountRepo{DB: tx}, Sessions: admins, Tokens: ops.JWTAccess{JWT: jwt}}}}).Register(router)

	usermarketing.API{Query: marketing.CouponQueryService{DB: tx}, Auth: userAuth}.Register(router)
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	w := call("POST", "/api/v1/admin/coupons", adminToken, `{"name":"registered marketing","discount_type":"amount","discount_value_cents":500,"valid_hours":24,"total_quota":2,"per_user_quota":1}`)
	var coupon struct {
		// Data 公开 HTTP 信封的业务载荷，仅用于响应形状断言。
		Data marketing.Coupon `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &coupon); err != nil || w.Code != 200 || coupon.Data.ID == 0 || coupon.Data.DiscountPercent != nil || coupon.Data.Status != "active" {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	path := "/api/v1/admin/coupons/" + strconv.FormatUint(coupon.Data.ID, 10)
	request := uuid.NewString()
	body := `{"request_id":"` + request + `","user_id":"` + strconv.FormatUint(id, 10) + `"}`
	w = call("POST", path+"/grants", adminToken, body)
	var first struct {
		// Data 公开 HTTP 信封的业务载荷，仅用于响应形状断言。
		Data marketing.GrantResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil || w.Code != 200 || first.Data.CouponGrantID == 0 {
		t.Fatalf("grant %d %s", w.Code, w.Body)
	}
	w = call("POST", path+"/grants", adminToken, body)
	var replay struct {
		// Data 公开 HTTP 信封的业务载荷，仅用于响应形状断言。
		Data marketing.GrantResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &replay); err != nil || w.Code != 200 || replay.Data.CouponGrantID != first.Data.CouponGrantID {
		t.Fatalf("replay %d %s", w.Code, w.Body)
	}
	w = call("POST", path+"/grants", adminToken, strings.Replace(body, request, uuid.NewString(), 1))
	if w.Code != 409 {
		t.Fatalf("quota %d %s", w.Code, w.Body)
	}
	w = call("GET", "/api/v1/user/coupon/my?only_usable=true", userToken, "")
	var mine struct {
		// Data 公开 HTTP 信封的业务载荷，仅用于响应形状断言。
		Data marketing.CouponPage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &mine); err != nil || w.Code != 200 || mine.Data.Total != 1 || mine.Data.Count != 1 || len(mine.Data.Items) != 1 || mine.Data.Items[0].GrantID != first.Data.CouponGrantID {
		t.Fatalf("mine %d %s", w.Code, w.Body)
	}
	w = call("GET", "/api/v1/user/coupons", userToken, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"grant_id":`+strconv.FormatUint(first.Data.CouponGrantID, 10)) {
		t.Fatalf("available %d %s", w.Code, w.Body)
	}
	now := time.Now().UTC()
	in := marketing.ActivityInput{Name: "registered activity", TriggerType: marketing.TriggerHoliday, CouponID: coupon.Data.ID, StartAt: now.Add(-time.Hour).Format(time.RFC3339), EndAt: now.Add(time.Hour).Format(time.RFC3339)}
	encoded, _ := json.Marshal(in)
	w = call("POST", "/api/v1/admin/coupon-activities", adminToken, string(encoded))
	var activity struct {
		// Data 公开 HTTP 信封的业务载荷，仅用于响应形状断言。
		Data struct {
			// ID 返回的业务记录主键。
			ID uint64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &activity); err != nil || w.Code != 200 || activity.Data.ID == 0 {
		t.Fatalf("activity %d %s", w.Code, w.Body)
	}
	in.TriggerType = marketing.TriggerFirstRecharge
	encoded, _ = json.Marshal(in)
	w = call("PUT", "/api/v1/admin/coupon-activities/"+strconv.FormatUint(activity.Data.ID, 10), adminToken, string(encoded))
	if w.Code != 400 {
		t.Fatalf("trigger %d %s", w.Code, w.Body)
	}
	in.TriggerType = marketing.TriggerInviteReward
	missing := id + 99
	in.InviterCouponID = &missing
	encoded, _ = json.Marshal(in)
	w = call("POST", "/api/v1/admin/coupon-activities", adminToken, string(encoded))
	if w.Code != 400 {
		t.Fatalf("inviter %d %s", w.Code, w.Body)
	}
	in.TriggerType = marketing.TriggerHoliday
	in.InviterCouponID = nil
	encoded, _ = json.Marshal(in)
	fault := errors.New("marketing projection unavailable")
	if err := tx.Callback().Query().Before("gorm:query").Register("test:marketing-query", func(db *gorm.DB) {
		if db.Statement.Table == "coupon" {
			db.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	w = call("POST", "/api/v1/admin/coupon-activities", adminToken, string(encoded))
	tx.Callback().Query().Remove("test:marketing-query")
	if w.Code != 503 {
		t.Fatalf("storage treated as invalid %d %s", w.Code, w.Body)
	}
	var auditJSON string
	if err := tx.Table("audit_log").Where("actor_id=? AND action='grant'", id+2).Order("id").Limit(1).Pluck("after_json", &auditJSON).Error; err != nil || !strings.Contains(auditJSON, strconv.FormatUint(id, 10)) {
		t.Fatal("grant identity audit", auditJSON, err)
	}
	if err := tx.Table("role_permission").Where("role_id=?", id+1).Delete(nil).Error; err != nil {
		t.Fatal(err)
	}
	w = call("GET", "/api/v1/admin/coupons", adminToken, "")
	if w.Code != 403 {
		t.Fatalf("revoked permission %d %s", w.Code, w.Body)
	}
}
