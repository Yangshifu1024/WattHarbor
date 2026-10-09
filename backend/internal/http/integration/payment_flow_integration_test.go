//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/money"
	authhttp "chargepilot/internal/http/auth"
	userpayment "chargepilot/internal/http/user/payment"
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// routeCheckoutFixture 仅替换设备规则和兼容性校验结果与渠道响应，支付请求及参数持久化使用实际 MySQL。
type routeCheckoutFixture struct {
	port        money.ScanResult // 固定可用端口，专用客户端另有真实 loopback 契约测试
	rule        charging.Rule    // 固定有效套餐，计价规则不由此 fixture 重写
	failed      bool             // 渠道本次是否丢失响应
	unavailable bool             // 首次申请后，模拟当前设备与方案不可读取
	calls       int              // 向渠道申请次数，持久参数恢复后不再调用
}

// ResolveScan 返回固定端口；配置读取故障时返回错误。
func (f *routeCheckoutFixture) ResolveScan(ctx context.Context, code string) (money.ScanResult, error) {
	if f.unavailable {
		return money.ScanResult{}, errors.New("device fixture unavailable")
	}
	return f.port, ctx.Err()
}

// ActiveDeviceRule 返回 ctx 下 stationID、deviceID 的固定方案，模拟故障只拒绝新请求。
func (f *routeCheckoutFixture) ActiveDeviceRule(ctx context.Context, stationID uint64, deviceID string) (charging.Rule, error) {
	if f.unavailable {
		return charging.Rule{}, errors.New("pricing fixture unavailable")
	}
	return f.rule, ctx.Err()
}

// CheckDeviceScheme 对 ctx 下目标设备与 scheme 返回取消状态；设备兼容性另由真实实现验证。
func (*routeCheckoutFixture) CheckDeviceScheme(ctx context.Context, stationID uint64, deviceID string, scheme charging.Scheme) error {
	return ctx.Err()
}

// Prepay 记录 req 的调用并返回受控渠道结果；丢失响应时不声明已获得可付款参数。
func (f *routeCheckoutFixture) Prepay(ctx context.Context, req payment.PrepayRequest) (payment.PrepayParams, error) {
	f.calls++
	if f.failed {
		return payment.PrepayParams{}, context.DeadlineExceeded
	}
	return payment.PrepayParams{Provider: "simulation", PrepayID: req.MerchantOrderNo, Package: "prepay_id=" + req.MerchantOrderNo}, ctx.Err()
}

// paymentRouteToken 为 userID 签发测试登录令牌；会话和用户状态检查沿用 fixture，t 报告签发错误。
func paymentRouteToken(t *testing.T, userID uint64) (authhttp.SessionAuthenticator, string) {
	t.Helper()
	jwt, err := auth.NewJWT("payment-route-secret-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(userID, 10), Kind: "user", OpenID: "route-user", SessionID: "payment-route", IssuedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return authhttp.SessionAuthenticator{Auth: authhttp.NewAuthenticator(jwt, walletSession{}, walletUser{})}, token
}

// TestRegisteredCheckoutPreservesOriginalRequestAfterUnknownPrepay 验证实际公开路由的严格输入、号恢复及金额 JSON。
// 设备条件校验和支付渠道使用测试替身，支付唯一键、申请和参数为真库；全部造数在外层事务中回滚。
func TestRegisteredCheckoutPreservesOriginalRequestAfterUnknownPrepay(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := testfixture.NewID()
	deviceID := "ROUTE-" + rand.Text()
	portID := deviceID + ":1"
	if err := tx.Table("user").Create(map[string]any{"id": id, "openid": "route-" + rand.Text(), "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
	// 意图过期扫描只访问本测试用户，防止外层事务锁住其他包的测试历史。
	// Scopes 会被一次查询消费，不能保证后续过期 UPDATE 仍按本测试用户筛选。
	// 在此独立连接的每次意图 UPDATE 前加同一条件，避免包间真库造数的范围锁互相阻塞。
	const expiryScope = "route_checkout_expiry_scope"
	if err := tx.Callback().Update().Before("gorm:update").Register(expiryScope, func(q *gorm.DB) {
		if q.Statement.Table == "charge_payment_intent" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "user_id", Value: id}}})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer tx.Callback().Update().Remove(expiryScope)
	scoped := tx.Session(&gorm.Session{})
	f := &routeCheckoutFixture{failed: true, port: money.ScanResult{Kind: "port", DeviceID: deviceID, StationID: 1, DeviceStatus: "enabled", Port: &money.ScanPort{PortID: portID, DeviceID: deviceID, PortNo: 1, Online: true, Available: true}}, rule: charging.Rule{ID: 42, StationID: 1, Version: 1, Spec: charging.Spec{Scheme: &charging.Scheme{Name: "路由套餐", Packages: []charging.Package{{ID: 1, Name: "时长", Mode: "duration", PriceCents: 400, Minutes: 60}}}}}}
	authn, token := paymentRouteToken(t, id)
	router := httpapi.NewRouter()
	userpayment.StartAPI{Auth: authn, Checkout: payment.CheckoutService{Ports: f, Pricing: f, Intents: payment.PaymentIntentRepo{DB: scoped}, Provider: f}}.Register(router)
	post := func(body, bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/user/scan/start", bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	in := payment.CheckoutInput{ClientRequestID: uuid.NewString(), PortID: portID, OfferID: 4201}
	encoded, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	if w := post(body, ""); w.Code != 401 {
		t.Fatalf("anonymous=%d %s", w.Code, w.Body.String())
	}
	for _, invalid := range []string{body + " {}", body[:len(body)-1] + `,"total_cents":1}`, body[:len(body)-1] + `,"client_request_id":"invalid"}`} {
		if w := post(invalid, token); w.Code != 400 {
			t.Fatalf("invalid=%d %s", w.Code, w.Body.String())
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid request reached channel")
	}
	if w := post(body, token); w.Code != 503 || !bytes.Contains(w.Body.Bytes(), []byte(`"code":3001`)) {
		t.Fatalf("unknown prepay=%d %s", w.Code, w.Body.String())
	}
	f.failed, f.unavailable = false, true
	w := post(body, token)
	var out struct {
		Code int                    `json:"code"` // 业务响应码
		Data payment.CheckoutResult `json:"data"` // 公开支付准备回执，不含 openid 或内部主键
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || out.Code != 0 || out.Data.PayableCents != 400 || out.Data.TotalCents != 400 || out.Data.PortID != portID || out.Data.MerchantOrderNo == "" {
		t.Fatalf("recovery=%d %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(`"openid"`)) || bytes.Contains(w.Body.Bytes(), []byte(`"payment_order_id"`)) {
		t.Fatal("private identity leaked")
	}
	replay := post(body, token)
	var repeated struct {
		Data payment.CheckoutResult `json:"data"` // 业务回执；请求追踪号允许每次请求不同
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &repeated); err != nil {
		t.Fatal(err)
	}
	if replay.Code != 200 || repeated.Data != out.Data || f.calls != 2 {
		t.Fatalf("replay=%d %s calls=%d", replay.Code, replay.Body.String(), f.calls)
	}
	in.OfferID++
	changed, _ := json.Marshal(in)
	if w := post(string(changed), token); w.Code != 409 {
		t.Fatalf("different content=%d %s", w.Code, w.Body.String())
	}
	var n int64
	if err := tx.Table("charge_order").Where("user_id=?", id).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("prepay created charge: count=%d err=%v", n, err)
	}
}

// routeNotificationFixture 返回固定验签结果，资金入账仍使用真实 MySQL。
type routeNotificationFixture struct {
	transaction payment.VerifiedTransaction // 已验签通知记录，可逐项变异身份
	err         error                       // 模拟验签失败，必须阻止任何资金写入
}

// VerifyNotification 返回预设验签结果。
func (f *routeNotificationFixture) VerifyNotification(ctx context.Context, request *http.Request) (payment.VerifiedTransaction, error) {
	if f.err != nil {
		return payment.VerifiedTransaction{}, f.err
	}
	return f.transaction, ctx.Err()
}

// TestRegisteredRechargeNotificationRejectsForeignPayerAndReplaysOnce 验证实际通知路由的身份拒绝、回滚及一次入账。
// 验签是 fixture；钱包、支付、摘要和 Outbox 为真实 MySQL，外层事务回滚全部造数。
func TestRegisteredRechargeNotificationRejectsForeignPayerAndReplaysOnce(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := testfixture.NewID()
	no := "PR" + rand.Text()
	openid := "payer-" + rand.Text()
	requestID := uuid.NewString()
	for _, row := range []struct {
		table  string         // 本测试的真实表
		values map[string]any // 测试支付单、钱包和退款申请所需字段
	}{{"user", map[string]any{"id": id, "openid": openid, "status": "active"}}, {"wallet_account", map[string]any{"id": id, "user_id": id, "balance_cents": 100, "frozen_cents": 0, "version": 1, "status": "frozen"}}, {"payment_order", map[string]any{"id": id, "order_no": no, "biz_type": "wallet_recharge", "biz_id": 0, "user_id": id, "pay_method": "wechat", "total_cents": 500, "paid_cents": 0, "status": "initiated", "created_month": "2026-10-01"}}, {"wallet_recharge_request", map[string]any{"request_id": requestID, "user_id": id, "amount_cents": 500, "payment_order_id": id}}} {
		if err := tx.Table(row.table).Create(row.values).Error; err != nil {
			t.Fatal(err)
		}
	}
	verified := payment.VerifiedTransaction{Provider: "wechat_direct", MerchantID: "merchant", AppID: "app", MerchantOrderNo: no, TransactionID: "TX-" + rand.Text(), OpenID: openid, PaidCents: 500, PaidAt: time.Now().UTC()}
	f := &routeNotificationFixture{transaction: verified}
	router := httpapi.NewRouter()
	userpayment.WechatCallbackAPI{Verifier: f, Callbacks: payment.CallbackService{DB: tx, ExpectedProvider: verified.Provider, ExpectedMerchantID: verified.MerchantID, ExpectedAppID: verified.AppID}}.Register(router)
	post := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/public/payments/wechat/callback", bytes.NewBufferString(`{}`)))
		return w
	}
	f.err = errors.New("signature fixture rejected")
	if w := post(); w.Code != 400 {
		t.Fatalf("signature=%d %s", w.Code, w.Body.String())
	}
	f.err = nil
	for _, alter := range []func(*payment.VerifiedTransaction){func(v *payment.VerifiedTransaction) { v.OpenID = "foreign" }, func(v *payment.VerifiedTransaction) { v.PaidCents++ }, func(v *payment.VerifiedTransaction) { v.MerchantID = "foreign" }, func(v *payment.VerifiedTransaction) { v.AppID = "foreign" }, func(v *payment.VerifiedTransaction) { v.Provider = "foreign" }} {
		f.transaction = verified
		alter(&f.transaction)
		if w := post(); w.Code != 409 {
			t.Fatalf("foreign identity=%d %s", w.Code, w.Body.String())
		}
	}
	f.transaction = verified
	for i := 0; i < 2; i++ {
		if w := post(); w.Code != 204 || w.Body.Len() != 0 {
			t.Fatalf("notification=%d %s", w.Code, w.Body.String())
		}
	}
	var walletRow struct {
		BalanceCents int64  // 余额 100 加一次到账 500
		Version      uint64 // 一次资金更新更新一次版本
		Status       string // 已冻结钱包仍接收已确认资金
	}
	if err := tx.Table("wallet_account").Where("id=?", id).Take(&walletRow).Error; err != nil {
		t.Fatal(err)
	}
	if walletRow.BalanceCents != 600 || walletRow.Version != 2 || walletRow.Status != "frozen" {
		t.Fatalf("credit=%+v", walletRow)
	}
	var count int64
	if err := tx.Table("wallet_txn").Where("user_id=? AND biz_ref=?", id, requestID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("ledger=%d err=%v", count, err)
	}
	f.transaction.TransactionID = "different-" + rand.Text()
	if w := post(); w.Code != 409 {
		t.Fatalf("different transaction=%d %s", w.Code, w.Body.String())
	}
}
