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
	userorder "chargepilot/internal/http/user/order"
	"chargepilot/internal/identity"
	"chargepilot/internal/ops"
	"chargepilot/internal/order"
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/sessiontoken"
	"chargepilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestRegisteredFeedbackRoutesPreserveOwnerTransitionsAndAudit 验证真实注册用户评价及后台列表／回复／关闭。
// 用户、订单、权限、审计及 Redis 会话为真实实现，DB 造数回滚并只删除本测试会话。
func TestRegisteredFeedbackRoutesPreserveOwnerTransitionsAndAudit(t *testing.T) {
	tx := cardRouteDB(t, "TEST_MYSQL_URL").Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	cache := identityRouteRedis(t)
	id := testfixture.NewID()
	tag := uuid.NewString()
	orderNo := "RF" + tag
	month := time.Now().UTC().Format("2006-01") + "-01"
	for _, seed := range []struct {
		table string         /* 所属事实表 */
		row   map[string]any /* 本测试事实 */
	}{
		{"user", map[string]any{"id": id, "openid": tag, "status": "active"}},
		{"user", map[string]any{"id": id + 1, "openid": tag + "-other", "status": "active"}},
		{"charge_order", map[string]any{"id": id, "order_no": orderNo, "user_id": id, "device_id": "RF" + tag, "port_no": 1, "status": "completed", "created_month": month}},
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
	service := order.FeedbackService{DB: tx}
	router := httpapi.NewRouter()
	userorder.FeedbackAPI{Feedback: service, Auth: authhttp.SessionAuthenticator{Auth: authhttp.NewAuthenticator(jwt, sessions, users)}}.Register(router)
	(adminorder.API{Faults: device.FaultService{DB: tx}, Feedback: service, Payments: payment.OrderQueryService{DB: tx}, Auth: authhttp.AdminAuth{Sessions: ops.SessionService{Accounts: ops.AccountRepo{DB: tx}, Sessions: adminSessions, Tokens: ops.JWTAccess{JWT: jwt}}}}).Register(router)
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	path := "/api/v1/user/charge/" + orderNo + "/feedback"
	body := "{\"rating\":5,\"category\":\"rating\",\"images\":[\"https://cdn.example.com/proof.jpg\"]}"
	if w := call("POST", path, body, ""); w.Code != 401 {
		t.Fatalf("anonymous=%d", w.Code)
	}
	if w := call("POST", path, body, other); w.Code != 404 {
		t.Fatalf("other business reference=%d", w.Code)
	}
	if w := call("POST", "/api/v1/user/charge/"+strconv.FormatUint(id, 10)+"/feedback", body, other); w.Code != 404 {
		t.Fatalf("other numeric identity=%d", w.Code)
	}
	for _, invalid := range []string{"{", strings.Replace(body, "https://", "http://", 1), strings.Replace(body, "\"rating\":5", "\"rating\":0", 1)} {
		if w := call("POST", path, invalid, owner); w.Code != 400 {
			t.Fatalf("invalid=%d", w.Code)
		}
	}
	w := call("POST", path, body, owner)
	if w.Code != 200 {
		t.Fatalf("submit=%d %s", w.Code, w.Body)
	}
	var submitted struct {
		Data struct {
			ID        uint64 `json:"feedback_id"` /* 原新反馈整数身份 */
			Submitted bool   `json:"submitted"`   /* 已提交标志 */
		} `json:"data"` /* 原成功结果 */
	}
	if err := json.Unmarshal(w.Body.Bytes(), &submitted); err != nil || submitted.Data.ID == 0 || !submitted.Data.Submitted {
		t.Fatal("feedback response changed")
	}
	if w := call("POST", path, body, owner); w.Code != 409 {
		t.Fatalf("repeat=%d", w.Code)
	}
	feedbackID := submitted.Data.ID
	adminPath := "/api/v1/admin/feedback/" + strconv.FormatUint(feedbackID, 10) + "/reply"
	w = call("GET", "/api/v1/admin/feedback?status=pending", "", adminToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "\"user_id\":\""+strconv.FormatUint(id, 10)+"\"") || !strings.Contains(w.Body.String(), "\"images\":[\"https://cdn.example.com/proof.jpg\"]") {
		t.Fatalf("list original shape=%d %s", w.Code, w.Body)
	}
	if w := call("POST", adminPath, "{\"action\":\"reply\",\"reply_content\":\"verified response\"}", adminToken); w.Code != 200 {
		t.Fatalf("reply=%d", w.Code)
	}
	if w := call("POST", adminPath, "{\"action\":\"reply\",\"reply_content\":\"again\"}", adminToken); w.Code != 409 {
		t.Fatalf("reply repeat=%d", w.Code)
	}
	fault := errors.New("registered feedback audit failure")
	const callback = "registered_feedback_audit_fault"
	if err := tx.Callback().Create().Before("gorm:create").Register(callback, func(q *gorm.DB) {
		if q.Statement.Table == "audit_log" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	w = call("POST", adminPath, "{\"action\":\"close\"}", adminToken)
	tx.Callback().Create().Remove(callback)
	if w.Code != 503 {
		t.Fatalf("audit failure HTTP=%d", w.Code)
	}
	status, err := (order.FeedbackRepo{}).LockStatus(tx, feedbackID)
	if err != nil || status != "processed" {
		t.Fatalf("failed close committed=%s %v", status, err)
	}
	for range 2 {
		if w := call("POST", adminPath, "{\"action\":\"close\"}", adminToken); w.Code != 200 {
			t.Fatalf("close replay=%d", w.Code)
		}
	}
	var audits int64
	if err := tx.Table("audit_log").Where("target_type='feedback' AND target_id=?", fmt.Sprint(feedbackID)).Count(&audits).Error; err != nil || audits != 2 {
		t.Fatalf("audit count=%d err=%v", audits, err)
	}
	// 拆除通用表名选择后，报修原注册路径仍调用所属 FaultRepo；本段只回归迁移表访问。
	if err := tx.Table("device_fault_report").Create(map[string]any{"id": id, "device_id": "RF" + tag, "user_id": id, "fault_type": "other"}).Error; err != nil {
		t.Fatal(err)
	}
	faultPath := "/api/v1/admin/device-fault-reports/" + strconv.FormatUint(id, 10)
	if w := call("GET", "/api/v1/admin/device-fault-reports?status=open", "", adminToken); w.Code != 200 || !strings.Contains(w.Body.String(), "\"id\":\""+strconv.FormatUint(id, 10)+"\"") {
		t.Fatalf("fault list=%d %s", w.Code, w.Body)
	}
	dispatch := fmt.Sprintf("{\"assigned_to\":%d,\"note\":\"verified dispatch\"}", id+3)
	for range 2 {
		if w := call("POST", faultPath+"/dispatch", dispatch, adminToken); w.Code != 200 {
			t.Fatalf("fault dispatch replay=%d %s", w.Code, w.Body)
		}
	}
	if w := call("POST", faultPath+"/resolve", "{\"status\":\"closed\"}", adminToken); w.Code != 409 {
		t.Fatalf("fault skip repair=%d", w.Code)
	}
	if w := call("POST", faultPath+"/resolve", "{\"status\":\"fixed\",\"note\":\"verified repair\"}", adminToken); w.Code != 200 {
		t.Fatalf("fault repair=%d", w.Code)
	}
	if w := call("POST", faultPath+"/resolve", "{\"status\":\"closed\"}", adminToken); w.Code != 200 {
		t.Fatalf("fault close=%d", w.Code)
	}
	if w := call("GET", faultPath+"/history", "", adminToken); w.Code != 200 || !strings.Contains(w.Body.String(), "\"total\":3") {
		t.Fatalf("fault history=%d %s", w.Code, w.Body)
	}
	if err := tx.Table("role_permission").Where("role_id=?", id+2).Delete(map[string]any{}).Error; err != nil {
		t.Fatal(err)
	}
	if w := call("POST", adminPath, "{\"action\":\"close\"}", adminToken); w.Code != 403 {
		t.Fatalf("revoked reply permission=%d", w.Code)
	}
}
