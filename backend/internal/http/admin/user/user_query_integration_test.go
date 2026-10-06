//go:build integration

package user

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/testfixture"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// TestRegisteredUserQueryKeepsBatchProjectionAndRechargeOwnership 验证真实后台注册权限、冻结用户档案及充值范围。
// 用户、钱包、订单、支付及后台权限是真库，Redis 会话是真实例；造数回滚且只撤销本测试 SID。
func TestRegisteredUserQueryKeepsBatchProjectionAndRechargeOwnership(t *testing.T) {
	tx := cardAdminDB(t).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := testfixture.NewID()
	tag := uuid.NewString()
	now := time.Now().UTC()
	month := now.Format("2006-01") + "-01"
	var permission uint64
	if err := tx.Table("permission").Where("code='user.read'").Pluck("id", &permission).Error; err != nil || permission == 0 {
		t.Fatalf("permission=%d %v", permission, err)
	}
	for _, seed := range []struct {
		table string         /* 所属事实表 */
		row   map[string]any /* 本测试记录 */
	}{
		{"role", map[string]any{"id": id + 30, "code": "identity-reader-" + tag, "name": "identity reader", "is_builtin": false}},
		{"role_permission", map[string]any{"role_id": id + 30, "permission_id": permission}},
		{"admin_user", map[string]any{"id": id + 31, "username": "identity-reader-" + tag, "password_hash": "fixture", "role_id": id + 30, "status": "active", "auth_version": 1}},
		{"wallet_account", map[string]any{"user_id": id, "balance_cents": 1000, "frozen_cents": 150, "status": "frozen"}},
		{"charge_order", map[string]any{"id": id, "order_no": "IQ" + tag, "user_id": id, "device_id": "IQ" + tag, "port_no": 1, "created_month": month, "status": "completed", "payment_status": "paid", "total_cents": 77}},
		{"device_fault_report", map[string]any{"id": id, "device_id": "IQ" + tag, "user_id": id, "fault_type": "other"}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatalf("seed %s: %v", seed.table, err)
		}
	}
	for i := range 11 {
		status := "active"
		if i == 0 {
			status = "frozen"
		}
		if err := tx.Table("user").Create(map[string]any{"id": id + uint64(i), "openid": fmt.Sprintf("query-%s-%d", tag, i), "nickname": "query-" + tag, "status": status}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		uid, biz := id, "wallet_recharge"
		if i == 1 {
			uid = id + 1
		}
		if i == 2 {
			biz = "charge"
		}
		if err := tx.Table("payment_order").Create(map[string]any{"id": id + uint64(i), "order_no": fmt.Sprintf("IP%s%d", tag, i), "user_id": uid, "biz_type": biz, "biz_id": id, "pay_method": "wechat", "total_cents": 300, "paid_cents": 100, "refunded_cents": 25, "status": "partial_refunded", "created_month": month}).Error; err != nil {
			t.Fatal(err)
		}
	}
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Fatal("TEST_REDIS_URL is required")
	}
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	cache := redis.NewClient(opts)
	defer cache.Close()
	sessions := ops.SessionRepo{Redis: cache}
	sid, _, err := sessions.Create(context.Background(), operations.AccountIdentity{ID: id + 31, AuthVersion: 1, Username: "identity-reader-" + tag})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sessions.RevokeSession(context.Background(), sid); err != nil {
			t.Error(err)
		}
	}()
	jwt, err := auth.NewJWT("identity-query-admin-secret-at-least-32")
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(id+31, 10), Kind: "admin", SessionID: sid, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	service := identity.UserQueryService{DB: tx, Recharges: wallet.RechargeHistoryService{Payments: payment.RechargeHistoryRepo{DB: tx}}}
	actor := operations.Profile{Permissions: []string{"user.read"}}
	// 列表仅两次分页查询与三次批量投影，增加当页人数不得增加 SQL 次数。
	queries := 0
	const countName = "identity_query_count"
	count := func(db *gorm.DB) { queries++ }
	if err := tx.Callback().Query().After("gorm:query").Register(countName, count); err != nil {
		t.Fatal(err)
	}
	if err := tx.Callback().Row().After("gorm:row").Register(countName, count); err != nil {
		t.Fatal(err)
	}
	defer tx.Callback().Query().Remove(countName)
	defer tx.Callback().Row().Remove(countName)
	for _, size := range []int{1, 20} {
		queries = 0
		out, err := service.List(context.Background(), actor, persist.PageQuery{Page: 1, PageSize: size, Keyword: "query-" + tag})
		if err != nil || out.Total != 11 || queries != 5 {
			t.Fatalf("list size=%d total=%d queries=%d err=%v", size, out.Total, queries, err)
		}
	}
	tx.Callback().Query().Remove(countName)
	tx.Callback().Row().Remove(countName)
	router := httpapi.NewRouter()
	API{Users: service, Auth: authhttp.AdminAuth{Sessions: ops.SessionService{Accounts: ops.AccountRepo{DB: tx}, Sessions: sessions, Tokens: ops.JWTAccess{JWT: jwt}}}}.Register(router)
	call := func(path, bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	path := "/api/v1/admin/users/" + strconv.FormatUint(id, 10)
	if w := call(path, ""); w.Code != 401 {
		t.Fatalf("anonymous=%d", w.Code)
	}
	w := call(path, token)
	if w.Code != 200 {
		t.Fatalf("detail=%d %s", w.Code, w.Body)
	}
	var detail struct {
		Data identity.ChargeUserDetail `json:"data"` /* 原档案公开数据 */
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	d := detail.Data
	if d.ID != id || d.Status != "frozen" || d.WalletStatus != "frozen" || d.BalanceCents != 1000 || d.FrozenCents != 150 || d.OrderCount != 1 || d.TotalCents != 77 || d.FaultReports != 1 || len(d.RecentOrders) != 1 || d.RecentOrders[0].BusinessStatus != "completed" {
		t.Fatalf("detail identity or projection changed: %+v", d)
	}
	w = call(path+"/recharges?page=1&page_size=10", token)
	if w.Code != 200 {
		t.Fatalf("recharges=%d %s", w.Code, w.Body)
	}
	var page struct {
		Data persist.Page[wallet.AdminRechargeRow] `json:"data"` /* 所属钱包历史页 */
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Data.Total != 1 || len(page.Data.Items) != 1 {
		t.Fatal("recharge leaked charge or other user's payment")
	}
	p := page.Data.Items[0]
	if p.UserID != id || p.BizType != "wallet_recharge" || p.PaidCents != 100 || p.RefundedCents != 25 || p.PaymentStatus != "partial_refunded" || p.ChargeOrderID != nil || p.ChargeOrderNo != nil {
		t.Fatalf("payment facts=%+v", p)
	}
	if !strings.Contains(w.Body.String(), "\"user_id\":\""+strconv.FormatUint(id, 10)+"\"") {
		t.Fatal("user identity lost string precision")
	}
	if w := call(path+"/recharges?user_id=1", token); w.Code != 400 {
		t.Fatalf("scope override=%d", w.Code)
	}
	if err := tx.Table("role_permission").Where("role_id=?", id+30).Delete(map[string]any{}).Error; err != nil {
		t.Fatal(err)
	}
	if w := call(path, token); w.Code != 403 {
		t.Fatalf("revoked permission=%d", w.Code)
	}
}
