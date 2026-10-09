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

	"chargepilot/internal/card"
	chargingquery "chargepilot/internal/charging/query"
	"chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/gateway/control"
	adminorder "chargepilot/internal/http/admin/order"
	authhttp "chargepilot/internal/http/auth"
	userorder "chargepilot/internal/http/user/order"
	"chargepilot/internal/identity"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/ops"
	"chargepilot/internal/order"
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/platform/sessiontoken"
	"chargepilot/internal/protocol"
	"chargepilot/internal/settlement"
	"chargepilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
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
	id := testfixture.NewID()
	tag := uuid.NewString()
	no := "OR" + tag
	month := time.Now().UTC().Format("2006-01") + "-01"
	for _, seed := range []struct {
		table string
		row   map[string]any
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
		Rule  charging.Rule  `json:"rule"`  /* 规则 */
		Offer charging.Offer `json:"offer"` /* 套餐 */
	}{Rule: charging.Rule{Version: 1, Spec: charging.Spec{Mode: charging.ModeDeviceDuration, Scheme: &charging.Scheme{Name: "Registered frozen scheme", Packages: []charging.Package{{ID: 1, Name: "Registered frozen package", Mode: "duration", PriceCents: 100, Minutes: 30}}}}}, Offer: charging.Offer{ID: 1, PackageID: 1, StationID: id, Name: "Registered frozen package", Mode: "duration", PriceCents: 100, DurationMinutes: 30}})
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
	adminSessions := ops.SessionRepo{Redis: cache}
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

	gatewayTx := cardRouteDB(t, "DATABASE_URL_GATEWAY").Begin()
	if gatewayTx.Error != nil {
		t.Fatal(gatewayTx.Error)
	}
	defer gatewayTx.Rollback()
	gatewayRouter := httpapi.NewRouter()
	control.TelemetryAPI{DB: gatewayTx, ServiceToken: "registered-live-query"}.Register(gatewayRouter)
	gatewayServer := httptest.NewServer(gatewayRouter)
	defer gatewayServer.Close()
	gatewayClient, err := gatewayclient.NewClient(serviceclient.Client{HTTP: gatewayServer.Client()}, gatewayServer.URL, "registered-live-query")
	if err != nil {
		t.Fatal(err)
	}
	liveService := chargingquery.LiveService{Gateway: gatewayClient, Orders: order.SnapshotRepo{DB: tx}, Cards: card.PurchaseQueryRepo{DB: tx}}
	service := order.QueryService{DB: tx, Fees: settlement.QueryFeeRepo{DB: tx}}
	router := httpapi.NewRouter()
	(userorder.QueryAPI{Orders: service, Query: chargingquery.UserService{Orders: service, Fees: settlement.QueryFeeRepo{DB: tx}, State: chargingquery.UserStateRepo{DB: tx}, Gateway: gatewayClient}, Auth: authhttp.SessionAuthenticator{Auth: authhttp.NewAuthenticator(jwt, sessions, users)}}).Register(router)
	(adminorder.API{Process: chargingquery.ProcessService{Orders: service, Gateway: gatewayClient}, Live: liveService, Query: service, Payments: payment.OrderQueryService{DB: tx}, Auth: authhttp.AdminAuth{Sessions: ops.SessionService{Accounts: ops.AccountRepo{DB: tx}, Sessions: adminSessions, Tokens: ops.JWTAccess{JWT: jwt}}}}).Register(router)
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
			Items []order.UserOrderHistoryRow `json:"items"` /* 订单行 */
			Total int64                       `json:"total"` /* 总数 */
		} `json:"data"` /* 页结果 */
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

	// 实际已注册后台详情必须输出补充后的实时行；确认费用回执不能被实时估算覆盖。
	liveStart := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Minute)
	if err := tx.Table("charge_order").Where("id=?", id).Updates(map[string]any{"status": "charging", "started_at": liveStart}).Error; err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 4; n++ {
		at := liveStart.Add(time.Duration(n*15) * time.Second)
		event := protocol.Event{Type: protocol.Heartbeat, DeviceID: "OR" + tag, ReceivedAt: at, ChargingPorts: []protocol.PortTelemetry{{Port: 1, ChargedSeconds: uint32(n * 15), ChargedMWh: uint32(n * 1000), PowerDeciWatts: 1500}, {Port: 2, ChargedSeconds: 3600, ChargedMWh: 1000000}}}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := gatewayTx.Table("device_event").Create(map[string]any{"event_key": tag + strconv.Itoa(n), "protocol_name": "dc589", "device_id": "OR" + tag, "event_type": "heartbeat", "event_json": string(raw), "received_at": at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	w = call(adminPath, adminToken)
	var liveReply struct {
		Data order.OrderView `json:"data"` /* 后台档案与实时估算 */
	}
	if err := json.Unmarshal(w.Body.Bytes(), &liveReply); err != nil || w.Code != 200 || liveReply.Data.Live == nil || liveReply.Data.Live.Seconds != 60 || liveReply.Data.Live.Fee == nil || liveReply.Data.Live.Fee.TotalCents != 3 || liveReply.Data.LiveUnavailable != "" {
		t.Fatalf("registered live detail=%d %s", w.Code, w.Body)
	}
	w = call("/api/v1/admin/orders?order_no="+no, adminToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "\"seconds\":60") || !strings.Contains(w.Body.String(), "\"total_cents\":3") {
		t.Fatalf("registered live list=%d %s", w.Code, w.Body)
	}
	var receipt settlement.ChargeFeeRecord
	if err := tx.Where("charge_order_id=?", id).Take(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	_, _, confirmed, ok := receipt.Fees()
	if !ok || confirmed != 0 {
		t.Fatal("live estimate changed confirmed receipt")
	}

	// 过程只使用已授权订单编号；额外公开设备参数不能覆盖设备与端口。
	for n := range 2 {
		if err := gatewayTx.Table("charge_process").Create(map[string]any{"id": id + 50 + uint64(n), "event_key": tag + "process" + strconv.Itoa(n), "charge_order_id": id, "order_no": no, "device_id": "OR" + tag, "port_no": 1, "ts": liveStart.Add(time.Duration(n) * time.Second), "power_deciwatts": 1485, "charged_seconds": 30 + n, "remaining_seconds": 120 - n, "charged_mwh": 2500, "remaining_mwh": 1250000, "signal_strength": 0, "port_status": 0, "temperature_c": -2}).Error; err != nil {
			t.Fatal(err)
		}
	}
	processPath := adminPath + "/process?limit=1&device_id=other&order_no=other"
	w = call(processPath, adminToken)
	var processReply struct {
		Data chargingquery.ProcessPage `json:"data"` /* 过程页 */
	}
	if err := json.Unmarshal(w.Body.Bytes(), &processReply); err != nil || w.Code != 200 || len(processReply.Data.Items) != 1 || processReply.Data.Items[0].ID != id+50 || processReply.Data.NextAfterID == nil || *processReply.Data.NextAfterID != id+50 || processReply.Data.Items[0].ChargedKWh != 0.0025 || processReply.Data.Items[0].PortStatus == nil || *processReply.Data.Items[0].PortStatus != 0 {
		t.Fatalf("registered canonical process=%d %s", w.Code, w.Body)
	}
	w = call(adminPath+"/process?limit=1&after_id="+strconv.FormatUint(*processReply.Data.NextAfterID, 10), adminToken)
	if err := json.Unmarshal(w.Body.Bytes(), &processReply); err != nil || w.Code != 200 || len(processReply.Data.Items) != 1 || processReply.Data.Items[0].ID != id+51 || processReply.Data.NextAfterID != nil {
		t.Fatalf("registered last process page=%d %s", w.Code, w.Body)
	}
	if w := call("/api/v1/admin/orders/"+strconv.FormatUint(id+99, 10)+"/process", adminToken); w.Code != 404 {
		t.Fatalf("missing process order=%d", w.Code)
	}

	// C 端组合读取使用真实两库和实际网关注册，确认零与补充观测保持独立。
	for _, seed := range []struct {
		table string /* 表 */
		row   map[string]any
	}{
		{"payment_order", map[string]any{"id": id, "order_no": "PAY" + tag, "biz_type": "charge", "biz_id": id, "user_id": id, "pay_method": "balance", "total_cents": 100, "paid_cents": 100, "refunded_cents": 99, "status": "partial_refunded", "created_month": month}},
		{"refund_record", map[string]any{"id": id, "refund_no": "RF" + tag, "payment_order_id": id, "user_id": id, "biz_type": "charge", "biz_id": id, "refund_cents": 99, "status": "success", "reason": "original query refund", "created_month": month}},
		{"charge_billing_job", map[string]any{"charge_order_id": id, "status": "manual_review"}},
		{"card_operation", map[string]any{"operation_id": uuid.NewString(), "device_id": "OR" + tag, "event_id": tag + "card", "charge_order_id": id, "card_id": id, "kind": "start", "price_cents": 100, "minutes": 30, "status": "confirmed"}},
		{"charge_billing_cutoff", map[string]any{"charge_order_id": id, "cutoff_at": liveStart.Add(time.Minute), "reason": "original cutoff"}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(seed.table, err)
		}
	}
	if err := tx.Table("charge_order").Where("id=?", id).Update("payment_order_id", id).Error; err != nil {
		t.Fatal(err)
	}
	observation := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Second)
	for _, metric := range []struct {
		port  uint8  /* 端口 */
		name  string /* 指标 */
		value string /* DECIMAL */
	}{{1, "power_w", "0"}, {1, "meter_kwh", "0.0025"}, {2, "power_w", "9999"}} {
		if err := gatewayTx.Table("telemetry").Create(map[string]any{"device_id": "OR" + tag, "port_no": metric.port, "metric": metric.name, "value_num": metric.value, "ts": observation}).Error; err != nil {
			t.Fatal(err)
		}
	}
	userDetail := "/api/v1/user/charge/" + no
	snapshotPath := "/api/v1/user/charge/ongoing/snapshot?order_id=" + no
	curvePath := "/api/v1/user/charge/ongoing/curve?order_id=" + no + "&window=last_30min"
	decodeView := func(w *httptest.ResponseRecorder) chargingquery.UserOrderView {
		t.Helper()
		var reply struct {
			Data chargingquery.UserOrderView `json:"data"` /* 本用例公开模型 */
		}
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || w.Code != 200 {
			t.Fatalf("user query=%d %s", w.Code, w.Body)
		}
		return reply.Data
	}
	userView := decodeView(call(userDetail, owner))
	if userView.TotalCents == nil || *userView.TotalCents != 0 || userView.Payment == nil || userView.Payment.OrderNo != "PAY"+tag || userView.Payment.RefundedCents != 99 || userView.Refunds == nil || len(*userView.Refunds) != 1 || len(userView.CardOperations) != 1 || userView.CardOperations[0].Minutes != 30 || userView.BillingStatus != "manual_review" || userView.BillingCutoff.CutoffAt == nil || userView.BillingCutoff.Reason != "original cutoff" || userView.Offer == nil {
		t.Fatalf("registered original detail=%+v", userView)
	}
	missingFee := decodeView(call("/api/v1/user/charge/"+no+"P", owner))
	if missingFee.TotalCents != nil || missingFee.Payment != nil || missingFee.Refunds == nil || len(*missingFee.Refunds) != 0 || !missingFee.StartConfirmationPending {
		t.Fatal("missing fee/payment shape changed")
	}
	userView = decodeView(call(snapshotPath, owner))
	if userView.TotalCents == nil || *userView.TotalCents != 0 || userView.TelemetryAvailable == nil || !*userView.TelemetryAvailable || userView.CurrentPowerW == nil || *userView.CurrentPowerW != 0 || userView.VoltageV != nil || userView.ChargedKWh != 0.0025 || userView.Payment != nil || userView.Refunds != nil {
		t.Fatalf("registered canonical snapshot=%+v", userView)
	}
	w = call(curvePath, owner)
	var curveReply struct {
		Data chargingquery.CurveView `json:"data"` /* 遥测模型 */
	}
	if err := json.Unmarshal(w.Body.Bytes(), &curveReply); err != nil || w.Code != 200 || curveReply.Data.Count != 1 || curveReply.Data.Series[0].PowerW == nil || *curveReply.Data.Series[0].PowerW != 0 || curveReply.Data.Series[0].VoltageV != nil {
		t.Fatalf("registered curve=%d %s", w.Code, w.Body)
	}
	for _, path := range []string{userDetail, snapshotPath, curvePath, userDetail + "/curve"} {
		if w := call(path, other); w.Code != 404 {
			t.Fatalf("other composite=%d %s", w.Code, w.Body)
		}
	}
	if w := call(curvePath+"bad", owner); w.Code != 400 || !strings.Contains(w.Body.String(), "1005") {
		t.Fatal("illegal window silently replaced")
	}
	bucket := liveStart.Truncate(15 * time.Minute)
	if err := gatewayTx.Table("telemetry_aggregate_15min").Create(map[string]any{"device_id": "OR" + tag, "port_no": 1, "metric": "power_w", "bucket_start": bucket, "bucket_month": month, "avg_value": "150.125", "min_value": "100.25", "max_value": "200.375", "count": 3}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("charge_order").Where("id=?", id).Updates(map[string]any{"status": "completed", "ended_at": liveStart.Add(time.Minute), "charged_kwh": "0.2500", "charged_seconds": 60}).Error; err != nil {
		t.Fatal(err)
	}
	w = call(userDetail+"/curve", owner)
	var historyReply struct {
		Data chargingquery.HistoricalView `json:"data"` /* 历史模型 */
	}
	if err := json.Unmarshal(w.Body.Bytes(), &historyReply); err != nil || w.Code != 200 || historyReply.Data.OrderID != id || historyReply.Data.OrderNo != no || len(historyReply.Data.Series) != 1 || string(historyReply.Data.Summary["total_kwh"]) != `"0.2500"` || !historyReply.Data.BoundaryApproximate {
		t.Fatalf("registered original history=%d %s", w.Code, w.Body)
	}
	var originalAverage string
	if err := json.Unmarshal(historyReply.Data.Summary["avg_power_w"], &originalAverage); err != nil {
		t.Fatal("registered original average missing", err)
	}
	if value, err := strconv.ParseFloat(originalAverage, 64); err != nil || value != 150.125 {
		t.Fatal("registered original average lost", originalAverage, err)
	}
	if w := call(userDetail+"/curve?granularity=minute", owner); w.Code != 400 {
		t.Fatal("legacy wrong documentation granularity accepted")
	}
	// 已结束订单以结束时刻计算包含首尾的桶数；不可用当前墙钟推断历史尾桶。
	// 使用完整过去桶，所有测试 phase 的结束都早于当前时刻，不触发网关未来窗口拒绝。
	historyEndBucket := liveStart.Add(time.Minute).Truncate(15 * time.Minute).Add(-15 * time.Minute)
	for _, phase := range []time.Duration{0, 8 * time.Minute, 14*time.Minute + 59*time.Second} {
		if err := tx.Table("charge_order").Where("id=?", id).Updates(map[string]any{"ended_at": historyEndBucket.Add(phase), "started_at": historyEndBucket.Add(-1999 * 15 * time.Minute)}).Error; err != nil {
			t.Fatal(err)
		}
		if w := call(userDetail+"/curve", owner); w.Code != 200 {
			t.Fatalf("2000 inclusive buckets phase=%s rejected=%d %s", phase, w.Code, w.Body)
		}
		if err := tx.Table("charge_order").Where("id=?", id).Update("started_at", historyEndBucket.Add(-2000*15*time.Minute)).Error; err != nil {
			t.Fatal(err)
		}
		if w := call(userDetail+"/curve", owner); w.Code != 400 {
			t.Fatalf("2001 inclusive buckets phase=%s accepted=%d %s", phase, w.Code, w.Body)
		}
	}
	if err := tx.Table("charge_order").Where("id=?", id).Updates(map[string]any{"status": "charging", "started_at": liveStart, "ended_at": nil, "charged_kwh": nil, "charged_seconds": nil}).Error; err != nil {
		t.Fatal(err)
	}
	// 真实查询故障须逐阶段返回 503；不隐藏费用或支付错误为未确认／无记录。
	for _, failure := range []struct {
		table   string /* 失败表 */
		message string /* 公开文案 */
	}{{"charge_fee_receipt", "费用暂时无法读取"}, {"charge_order_pricing", "订单方案及确认状态暂不可读取"}, {"payment_order", "支付记录暂时无法读取"}, {"refund_record", "退款记录暂时无法读取"}} {
		name := "registered_user_query_fault_" + failure.table
		if err := tx.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
			if q.Statement.Table == failure.table {
				q.AddError(errors.New("query storage failure"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		w = call(userDetail, owner)
		tx.Callback().Query().Remove(name)
		if w.Code != 503 || !strings.Contains(w.Body.String(), failure.message) {
			t.Fatalf("%s hidden=%d %s", failure.table, w.Code, w.Body)
		}
	}
	gatewayServer.Close()
	if w := call(adminPath+"/process", adminToken); w.Code != 503 || !strings.Contains(w.Body.String(), "充电过程暂时无法读取，请稍后重试") {
		t.Fatalf("registered process dependency error=%d %s", w.Code, w.Body)
	}
	userView = decodeView(call(snapshotPath, owner))
	if userView.TelemetryAvailable == nil || *userView.TelemetryAvailable || userView.TotalCents == nil || *userView.TotalCents != 0 {
		t.Fatal("offline snapshot discarded confirmed state")
	}
	for _, path := range []string{curvePath, userDetail + "/curve"} {
		if w := call(path, owner); w.Code != 503 {
			t.Fatalf("offline curve became empty=%d %s", w.Code, w.Body)
		}
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
	if w := call(adminPath+"/process", adminToken); w.Code != 403 {
		t.Fatalf("revoked process permission=%d", w.Code)
	}
}
