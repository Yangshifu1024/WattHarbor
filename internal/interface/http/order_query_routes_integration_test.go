//go:build integration

package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/identity"
	sessionstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/redis"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	adminorder "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/order"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/sessiontoken"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRegisteredOrderQueryKeepsOriginalPagesAndFrozenDetail 验证真实注册用户历史／当前状态及后台订单列表／详情／时间线。
// 订单、费用、快照、站点和权限是真库，会话是真 Redis；全部 DB 造数回滚，Redis 仅删除自身 SID。
func TestRegisteredOrderQueryKeepsOriginalPagesAndFrozenDetail(t *testing.T) {
	tx := cardRouteDB(t, "TEST_MYSQL_URL").Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	cache := identityRouteRedis(t)
	id := uint64(time.Now().UnixNano())
	tag := uuid.NewString()
	no := "OR" + tag
	month := time.Now().UTC().Format("2006-01") + "-01"
	for _, seed := range []struct {
		table string         /* 所属事实表 */
		row   map[string]any /* 自身原事实 */
	}{
		{"user", map[string]any{"id": id, "openid": tag, "status": "active"}},
		{"user", map[string]any{"id": id + 1, "openid": tag + "-other", "status": "active"}},
		{"station", map[string]any{"id": id, "name": "Registered original station", "longitude": 0, "latitude": 0, "status": "active"}},
		{"role", map[string]any{"id": id + 3, "code": "order-reader-" + tag, "name": "order reader", "is_builtin": false}},
		{"admin_user", map[string]any{"id": id + 4, "username": "order-reader-" + tag, "password_hash": "fixture", "role_id": id + 3, "status": "active", "auth_version": 1}},
		{"charge_order", map[string]any{"id": id, "order_no": no, "user_id": id, "device_id": "OR" + tag, "port_no": 1, "status": "completed", "created_month": month}},
		{"charge_order", map[string]any{"id": id + 1, "order_no": no + "P", "user_id": id, "device_id": "OR" + tag, "port_no": 2, "status": "paid", "created_month": month}},
		{"charge_order", map[string]any{"id": id + 2, "order_no": no + "O", "user_id": id + 1, "device_id": "OR" + tag, "port_no": 3, "status": "charging", "created_month": month}},
		{"charge_fee_receipt", map[string]any{"charge_order_id": id, "calculation_no": "OB" + tag, "result_json": "{\"electric_cents\":0,\"service_cents\":0,\"total_cents\":0}"}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatalf("seed %s %v", seed.table, err)
		}
	}
	var permission uint64
	if err := tx.Table("permission").Where("code='order.read'").Pluck("id", &permission).Error; err != nil || permission == 0 {
		t.Fatal("order permission missing")
	}
	if err := tx.Table("role_permission").Create(map[string]any{"role_id": id + 3, "permission_id": permission}).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal(struct {
		Rule  charging.Rule  `json:"rule"`  /* 原规则 */
		Offer charging.Offer `json:"offer"` /* 原套餐 */
	}{Rule: charging.Rule{Version: 1, Spec: charging.Spec{Scheme: &charging.Scheme{Name: "Registered frozen scheme"}}}, Offer: charging.Offer{ID: 1, StationID: id, Name: "Registered frozen package", Mode: "duration", PriceCents: 100, DurationMinutes: 30}})
	if err != nil {
		t.Fatal(err)
	}
	intent := uuid.NewString()
	if err := tx.Table("charge_order_pricing").Create(map[string]any{"charge_order_id": id, "payment_intent_id": intent, "user_id": id, "port_code": tag, "pricing_snapshot": string(snapshot)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("charge_payment_intent").Create(map[string]any{"intent_id": intent, "client_request_id": uuid.NewString(), "merchant_order_no": "OP" + tag, "payment_order_id": id, "user_id": id, "openid": tag, "device_id": "OR" + tag, "port_no": 1, "port_code": tag, "station_id": id, "pricing_rule_id": id, "pricing_rule_version": 1, "pricing_snapshot": string(snapshot), "estimated_kwh": 0, "estimated_minutes": 30, "electric_cents": 100, "service_cents": 0, "total_cents": 100, "charge_mode": 4, "charge_quantity": 300, "status": "paid", "expires_at": time.Now().Add(time.Hour), "charge_order_id": id}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("charge_event_log").Create(map[string]any{"charge_order_id": id, "event_id": tag, "event": "proof", "actor": "query", "detail": "registered original event", "occurred_at": time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	jwt, err := auth.NewJWT("registered-order-query-secret-at-least-32")
	if err != nil {
		t.Fatal(err)
	}
	users := identity.UserRepo{DB: tx}
	sessions := identity.SessionRepo{Redis: cache}
	layout := sessiontoken.Layout{KeyPrefix: "user:session:"}
	userToken := func(uid uint64, openid string) string {
		sid, _, err := sessions.Create(context.Background(), identity.User{ID: uid, OpenID: openid})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := cache.Del(context.Background(), layout.Key(sid)).Err(); err != nil {
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
	adminSessions := sessionstore.AdminSessions{Redis: cache}
	sid, _, err := adminSessions.Create(context.Background(), operations.AccountIdentity{ID: id + 4, AuthVersion: 1, Username: "order-reader-" + tag})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := adminSessions.RevokeSession(context.Background(), sid); err != nil {
			t.Error(err)
		}
	})
	adminToken, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(id+4, 10), Kind: "admin", SessionID: sid, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	service := order.QueryService{DB: tx, Fees: settlement.QueryFeeRepo{DB: tx}}
	router := httpapi.NewRouter()
	(chargehttp.UserQueryAPI{Orders: service, DB: tx, Auth: chargehttp.SessionAuthenticator{Auth: chargehttp.NewAuthenticator(jwt, sessions, users)}}).Register(router)
	(adminorder.API{Query: service, DB: tx, Auth: chargehttp.AdminAuth{Store: ops.AdminStore{DB: tx}, JWT: jwt, Sessions: adminSessions}}).Register(router)
	call := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	history := "/api/v1/user/charge/history?page=1&page_size=20"
	if w := call(history, ""); w.Code != 401 {
		t.Fatalf("anonymous history=%d", w.Code)
	}
	w := call(history, owner)
	if w.Code != 200 {
		t.Fatalf("history=%d %s", w.Code, w.Body)
	}
	var page struct {
		Data struct {
			Items []order.UserOrderHistoryRow `json:"items"` /* 原订单行 */
			Total int64                       `json:"total"` /* 原总数 */
		} `json:"data"` /* 原页结果 */
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Data.Total != 2 || len(page.Data.Items) != 2 || page.Data.Items[0].OrderNo != no+"P" || page.Data.Items[0].TotalCents != nil || page.Data.Items[1].TotalCents == nil || *page.Data.Items[1].TotalCents != 0 {
		t.Fatal("original history owner, ordering or nullable fees changed")
	}
	w = call(history, other)
	if w.Code != 200 || strings.Contains(w.Body.String(), "\"order_no\":\""+no+"\"") {
		t.Fatalf("other history=%d", w.Code)
	}
	if w := call("/api/v1/user/charge/ongoing", owner); w.Code != 200 || !strings.Contains(w.Body.String(), no+"P") {
		t.Fatalf("owned ongoing=%d %s", w.Code, w.Body)
	}
	if w := call("/api/v1/user/charge/"+no+"/curve", other); w.Code != 404 {
		t.Fatalf("other curve owner=%d", w.Code)
	}
	if w := call("/api/v1/user/charge/history?status=unknown", owner); w.Code != 400 {
		t.Fatalf("invalid status=%d", w.Code)
	}
	adminPath := "/api/v1/admin/orders/" + strconv.FormatUint(id, 10)
	w = call("/api/v1/admin/orders?order_no="+no, adminToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Registered original station") || !strings.Contains(w.Body.String(), "Registered frozen scheme") {
		t.Fatalf("admin list=%d %s", w.Code, w.Body)
	}
	w = call(adminPath, adminToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Registered frozen package") || !strings.Contains(w.Body.String(), "Registered original station") {
		t.Fatalf("admin frozen detail=%d %s", w.Code, w.Body)
	}
	w = call(adminPath+"/timeline", adminToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "registered original event") {
		t.Fatalf("timeline=%d %s", w.Code, w.Body)
	}
	if w := call("/api/v1/admin/orders/"+strconv.FormatUint(id+99, 10)+"/timeline", adminToken); w.Code != 404 {
		t.Fatalf("missing timeline=%d", w.Code)
	}
	fault := errors.New("registered order fee fault")
	const callback = "registered_order_fee_fault"
	if err := tx.Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
		if q.Statement.Table == "charge_fee_receipt" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	w = call(history, owner)
	tx.Callback().Query().Remove(callback)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "费用暂时无法读取") {
		t.Fatalf("fee failure HTTP=%d", w.Code)
	}
	if err := tx.Table("role_permission").Where("role_id=?", id+3).Delete(map[string]any{}).Error; err != nil {
		t.Fatal(err)
	}
	if w := call(adminPath, adminToken); w.Code != 403 {
		t.Fatalf("revoked order permission=%d", w.Code)
	}
}
