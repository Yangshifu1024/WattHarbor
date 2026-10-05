//go:build integration

package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/card"
	"github.com/ChargePilot2026/charge-pilot/internal/charging/stop"
	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/control"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/identity"
	centralclient "github.com/ChargePilot2026/charge-pilot/internal/integration/central"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// cardRouteDB 打开 env 指定的真实库交由 t 关闭；缺配置或失败立即终止，不跳过。
func cardRouteDB(t *testing.T, env string) *gorm.DB {
	t.Helper()
	url := os.Getenv(env)
	if url == "" {
		t.Fatalf("%s is required", env)
	}
	db, err := dbconn.Open(context.Background(), url)
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

// cardRouteSessions 只模拟会话存活；JWT 及当前账号状态使用真实实现，不证明 Redis 生命周期。
type cardRouteSessions struct{}

// Exists 返回 ctx 下 sid 非空的会话 fixture，无外部 IO。
func (cardRouteSessions) Exists(ctx context.Context, sid string) (bool, error) { return sid != "", nil }

// sameCardJSON 对 a/b 使用精确 JSON 数字解码后规范序列化，返回字段和值是否完全相同。
// MySQL JSON 会重排键和空白；文本格式不作为重放身份，无 IO，非法 JSON 终止测试。
func sameCardJSON(t *testing.T, a, b string) bool {
	t.Helper()
	canonical := func(raw string) string {
		var value any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	return canonical(a) == canonical(b)
}

// TestRegisteredCardRoutesUseOwnedServicesAndNamedClients 验证实际注册 user/central/gateway 端点及两真库。
// 所有造数外层回滚；网关在线是持久事实 fixture，不代表 TCP 或实机启动确认。
func TestRegisteredCardRoutesUseOwnedServicesAndNamedClients(t *testing.T) {
	central := cardRouteDB(t, "TEST_MYSQL_URL").Begin()
	defer central.Rollback()
	gateway := cardRouteDB(t, "DATABASE_URL_GATEWAY").Begin()
	defer gateway.Rollback()
	if central.Error != nil || gateway.Error != nil {
		t.Fatalf("begin: %v %v", central.Error, gateway.Error)
	}
	id := uint64(time.Now().UnixNano())
	number := strconv.FormatUint(id%4000000000+1, 10)
	deviceID := "CR" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	port := deviceID + "-1"
	port2 := deviceID + "-2"
	key := uuid.NewString()
	otherOpenID := uuid.NewString()
	scheme := charging.Scheme{Name: "registered card proof", Packages: []charging.Package{{ID: 1, Name: "half hour", Mode: "duration", PriceCents: 100, Minutes: 30}}, Card: charging.CardPolicy{PackageID: 1, MaxMinutes: 180}}
	raw, _ := json.Marshal(charging.Spec{Scheme: &scheme})
	eventRaw, _ := json.Marshal(protocol.Event{EventID: key, Type: protocol.CardBalanceQuery, DeviceID: deviceID, CardNumber: uint32(id%4000000000 + 1), ReceivedAt: time.Now()})
	for _, seed := range []struct {
		db    *gorm.DB
		table string
		row   map[string]any
	}{
		{central, "user", map[string]any{"id": id, "openid": key, "status": "active"}}, {central, "user", map[string]any{"id": id + 1, "openid": otherOpenID, "status": "active"}}, {central, "wallet_account", map[string]any{"user_id": id, "balance_cents": 1000, "status": "active"}}, {central, "online_card", map[string]any{"id": id, "card_no": number, "user_id": id, "status": "active"}}, {central, "station", map[string]any{"id": id, "name": deviceID, "longitude": 0, "latitude": 0, "status": "active"}}, {central, "device_meta", map[string]any{"device_id": deviceID, "station_id": id, "vendor_id": id, "protocol_adapter": "dc589", "status": "enabled"}}, {central, "pricing_rule", map[string]any{"name": deviceID, "station_id": id, "spec_json": string(raw), "channel": "default", "version": 1, "status": "active"}},
		{gateway, "vendor", map[string]any{"id": id, "vendor_code": deviceID, "vendor_name": "registered card gateway", "adapter_class": "dc589", "protocol": "tcp", "status": "enabled"}}, {gateway, "device", map[string]any{"device_id": deviceID, "vendor_id": id, "station_id": id, "port_count": 2, "status": "enabled", "last_seen_at": time.Now()}}, {gateway, "device_port", map[string]any{"device_id": deviceID, "port_no": 1, "port_code": port, "status": "idle"}}, {gateway, "device_port", map[string]any{"device_id": deviceID, "port_no": 2, "port_code": port2, "status": "idle"}}, {gateway, "device_event", map[string]any{"event_key": key, "protocol_name": "dc589", "device_id": deviceID, "event_type": "card_balance_query", "event_json": string(eventRaw), "received_at": time.Now()}}, {gateway, "card_event_delivery", map[string]any{"event_key": key, "status": "pending", "next_attempt_at": time.Now().Add(-time.Minute)}},
	} {
		if err := seed.db.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatalf("seed %s: %v", seed.table, err)
		}
	}
	const token = "registered-card-service"
	edgeRouter := httpapi.NewRouter()
	(control.ScanAPI{Store: store.MySQLSink{DB: gateway}, ServiceToken: token}).Register(edgeRouter)
	(control.TelemetryAPI{DB: gateway, ServiceToken: token}).Register(edgeRouter)
	edgeServer := httptest.NewServer(edgeRouter)
	defer edgeServer.Close()
	edge, err := gatewayclient.NewClient(serviceclient.Client{}, edgeServer.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	jwt, err := auth.NewJWT("registered-card-user-secret-at-least-32")
	if err != nil {
		t.Fatal(err)
	}
	userAuth := chargehttp.NewAuthenticator(jwt, cardRouteSessions{}, identity.UserRepo{DB: central})
	router := httpapi.NewRouter()
	api := chargehttp.CardAPI{Swipe: card.SwipeService{DB: central, Ports: device.DiscoveryService{Gateway: edge, Operations: device.OperationRepo{DB: central}}, Devices: device.OperationRepo{DB: central}, Pricing: pricing.Repo{DB: central}, Cutoff: stop.CutoffRepo{}}, Status: card.CardStatusService{DB: central}, Auth: chargehttp.SessionAuthenticator{Auth: userAuth}, ServiceToken: token}
	api.RegisterInternal(router)
	api.RegisterUser(router)
	server := httptest.NewServer(router)
	defer server.Close()
	core, err := centralclient.NewClient(serviceclient.Client{}, server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, bearer, serviceToken string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if serviceToken != "" {
			req.Header.Set("X-Service-Token", serviceToken)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	if w := call("POST", centralcontract.CardSwipePath, "{}", "", ""); w.Code != 401 {
		t.Fatalf("anonymous internal=%d %s", w.Code, w.Body)
	}
	if w := call("POST", centralcontract.CardSwipePath, "{", "", token); w.Code != 400 {
		t.Fatalf("malformed=%d %s", w.Code, w.Body)
	}
	in := centralcontract.CardSwipeRequest{CardNo: number, EventID: uuid.NewString(), PortID: port, OccurredAt: time.Now()}
	first, err := core.SwipeCard(context.Background(), in)
	if err != nil || first.EventID != in.EventID || first.DeviceID != deviceID || first.Kind != "start" || first.PriceCents != 100 {
		t.Fatalf("registered swipe=%+v %v", first, err)
	}
	if err := central.Table("device_meta").Where("device_id=?", deviceID).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	in.OccurredAt = time.Now().Add(-time.Hour)
	replayed, err := core.SwipeCard(context.Background(), in)
	if err != nil || replayed.OperationID != first.OperationID {
		t.Fatalf("old frozen operation replay=%+v %v", replayed, err)
	}
	in.EventID = uuid.NewString()
	in.OccurredAt = time.Now()
	if _, err := core.SwipeCard(context.Background(), in); !errors.Is(err, centralcontract.ErrCardDeclined) {
		t.Fatalf("paused=%v", err)
	}
	if err := central.Table("device_meta").Where("device_id=?", deviceID).Update("status", "enabled").Error; err != nil {
		t.Fatal(err)
	}
	if err := central.Table("wallet_account").Where("user_id=?", id).Update("balance_cents", 1).Error; err != nil {
		t.Fatal(err)
	}
	in.EventID = uuid.NewString()
	in.PortID = port2
	_, err = core.SwipeCard(context.Background(), in)
	var declined *centralcontract.CardDeclineReply
	if !errors.As(err, &declined) || !declined.InsufficientBalance {
		t.Fatalf("insufficient=%v", err)
	}
	if balance, err := core.CardBalance(context.Background(), number); err != nil || !balance.Valid || balance.BalanceCents != 1 {
		t.Fatalf("registered balance=%+v %v", balance, err)
	}
	tokenFor := func(user uint64) string {
		openid := key
		if user != id {
			openid = otherOpenID
		}
		value, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(user, 10), Kind: "user", OpenID: openid, SessionID: key, IssuedAt: time.Now().Add(-time.Minute).Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	own, other := tokenFor(id), tokenFor(id+1)
	if w := call("GET", "/api/v1/user/cards", "", "", ""); w.Code != 401 {
		t.Fatalf("anonymous cards=%d", w.Code)
	}
	if w := call("GET", "/api/v1/user/cards", "", other, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("other user list=%d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/v1/user/cards/"+number+"/status", `{"status":"lost"}`, other, ""); w.Code != 404 {
		t.Fatalf("other status=%d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/v1/user/cards/"+number+"/status", `{"status":"active"}`, own, ""); w.Code != 400 {
		t.Fatalf("user reactivation=%d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/v1/user/cards/"+number+"/status", `{"status":"lost"}`, own, ""); w.Code != 200 {
		t.Fatalf("lost=%d %s", w.Code, w.Body)
	}
	if balance, err := core.CardBalance(context.Background(), number); err != nil || balance.Valid || balance.BalanceCents != 0 {
		t.Fatalf("lost balance=%+v %v", balance, err)
	}
	if err := central.Table("user").Where("id=?", id).Update("status", "frozen").Error; err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/api/v1/user/cards", "", own, ""); w.Code != 403 {
		t.Fatalf("revoked user=%d %s", w.Code, w.Body)
	}
	listed, err := edge.CardEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range listed {
		if row.EventKey == key {
			found = sameCardJSON(t, row.Payload, string(eventRaw)) && row.Response == ""
		}
	}
	if !found {
		t.Fatalf("original event missing: %+v", listed)
	}
	reply := gatewaycontract.CardReplyRequest{DeviceID: deviceID, Session: "010203040506", Kind: protocol.CommandCardBalance, CardNumber: 17, BalanceUnits: 88}
	replyRaw, _ := json.Marshal(reply)
	frozen, err := edge.FreezeCardResponse(context.Background(), gatewaycontract.CardFreezeRequest{EventKey: key, ResponseJSON: string(replyRaw)})
	if err != nil || !frozen.Stored {
		t.Fatalf("freeze=%+v %v", frozen, err)
	}
	reply.BalanceUnits = 999
	changed, _ := json.Marshal(reply)
	again, err := edge.FreezeCardResponse(context.Background(), gatewaycontract.CardFreezeRequest{EventKey: key, ResponseJSON: string(changed)})
	if err != nil || again.Stored || !sameCardJSON(t, again.ResponseJSON, frozen.ResponseJSON) {
		t.Fatalf("original freeze overwritten=%+v %v", again, err)
	}
	if err := edge.AdvanceCardEvent(context.Background(), gatewaycontract.CardAdvanceRequest{EventKey: key, Outcome: "retry", Error: "response lost"}); err != nil {
		t.Fatal(err)
	}
	var delivery struct {
		Attempts     uint32
		Status       string
		ResponseJSON string
	}
	if err := gateway.Table("card_event_delivery").Where("event_key=?", key).Take(&delivery).Error; err != nil || delivery.Attempts != 1 || delivery.Status != "pending" || !sameCardJSON(t, delivery.ResponseJSON, frozen.ResponseJSON) {
		t.Fatalf("retry state=%+v %v", delivery, err)
	}
	if resolved, err := edge.ResolveCardPort(context.Background(), deviceID, 1); err != nil || !resolved.Found || resolved.PortCode != port {
		t.Fatalf("registered port=%+v %v", resolved, err)
	}
	if err := edge.AdvanceCardEvent(context.Background(), gatewaycontract.CardAdvanceRequest{EventKey: key, Outcome: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := gateway.Table("card_event_delivery").Where("event_key=?", key).Take(&delivery).Error; err != nil || delivery.Status != "done" || !sameCardJSON(t, delivery.ResponseJSON, frozen.ResponseJSON) {
		t.Fatalf("done changed freeze=%+v %v", delivery, err)
	}
}
