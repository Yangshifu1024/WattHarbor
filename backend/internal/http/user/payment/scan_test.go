package payment

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/device"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// scanSession 是支付路由测试的固定有效会话能力，用于隔离 Redis 会话读取，不创建真实会话。
type scanSession struct{}

// Exists 忽略输入 context 和 SID，返回 true、nil，以隔离支付报价测试会话依赖；不访问 Redis。
func (scanSession) Exists(context.Context, string) (bool, error) { return true, nil }

// scanUser 是支付路由测试的固定有效用户能力，用于隔离账号状态读取，不查询真实用户。
type scanUser struct{}

// Active 忽略输入 context 和用户编号，返回 true、nil，以隔离支付报价测试用户依赖；不访问数据库。
func (scanUser) Active(context.Context, uint64) (bool, error) { return true, nil }

// scanOperation 以字符串指定设备运行状态或读取错误，用于扫码发现的准入映射；不替代网关返回的物理端口状态。
type scanOperation string

// DeviceStatus 忽略 context 和设备编码，返回 s 指定的运行状态或 error 对应读取错误；不访问数据库，仅验证设备发现的准入映射。
func (s scanOperation) DeviceStatus(context.Context, string) (string, error) {
	if s == "error" {
		return "", errors.New("storage unavailable")
	}
	return string(s), nil
}

// TestScanIsPublicAndReturnsOnlyGatewayReadResult 用 t 验证真实注册的匿名扫码及端口路由与测试 HTTP gateway 契约；核对服务令牌及公开读模型，不访问真库或设备。
func TestScanIsPublicAndReturnsOnlyGatewayReadResult(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/scan/resolve" || r.URL.Query().Get("code") != "board:1" || r.Header.Get("X-Service-Token") != "service-token" {
			t.Errorf("unexpected gateway request %s", r.URL.String())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"port","device_id":"board","port":{"port_id":"board:1","device_id":"board","port_no":1,"port_status":"idle","online":true,"available":true}}}`))
	}))
	defer backend.Close()
	jwt, err := auth.NewJWT("test-scan-secret-must-have-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, err := jwt.Sign(auth.Claims{Subject: "7", Kind: "user", OpenID: "openid", SessionID: "session", IssuedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := httpapi.NewRouter()
	ScanAPI{Discovery: testScanDiscovery(t, backend, "service-token", scanOperation("enabled"))}.Register(router)
	for _, route := range []string{"/api/v1/user/scan/resolve", "/api/v1/user/scan/port"} {
		body := `{"code":"board:1"}`
		if route == "/api/v1/user/scan/port" {
			body = `{"port_id":"board:1"}`
		}
		unauth := httptest.NewRecorder()
		anonymous := httptest.NewRequest(http.MethodPost, route, bytes.NewBufferString(body))
		anonymous.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(unauth, anonymous)
		if unauth.Code != http.StatusOK || !bytes.Contains(unauth.Body.Bytes(), []byte(`"available":true`)) {
			t.Fatalf("%s anonymous status=%d body=%s", route, unauth.Code, unauth.Body.String())
		}
		request := httptest.NewRequest(http.MethodPost, route, bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"available":true`)) {
			t.Fatalf("%s status=%d body=%s", route, response.Code, response.Body.String())
		}
	}
}

// TestScanShowsOperationalStateAndFailsClosed 用 t 验证扫码发现对窄运行态 fixture 的状态组合及错误关闭行为；使用测试 HTTP gateway，不证明真实数据库或设备状态。
func TestScanShowsOperationalStateAndFailsClosed(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"device","device_id":"board","ports":[{"port_id":"board:1","device_id":"board","port_no":1,"port_status":"idle","online":true,"available":true},{"port_id":"board:2","device_id":"board","port_no":2,"port_status":"charging","online":true,"available":false}]}}`))
	}))
	defer backend.Close()
	for _, status := range []string{"enabled", "disabled", "fault", "retired", "error"} {
		router := httpapi.NewRouter()
		ScanAPI{Discovery: testScanDiscovery(t, backend, "service", scanOperation(status))}.Register(router)
		response := httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/api/v1/user/scan/resolve", bytes.NewBufferString(`{"code":"board"}`))
		router.ServeHTTP(response, request)
		if status == "error" {
			if response.Code != 503 {
				t.Fatalf("operation outage: %d", response.Code)
			}
			continue
		}
		if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"device_status":"`+status+`"`)) {
			t.Fatalf("%s: %s", status, response.Body.String())
		}
		if status != "enabled" && bytes.Contains(response.Body.Bytes(), []byte(`"available":true`)) {
			t.Fatalf("disabled device selectable: %s", response.Body.String())
		}
		if !bytes.Contains(response.Body.Bytes(), []byte(`"port_status":"charging"`)) {
			t.Fatal("operational state erased physical charging state")
		}
	}
	_, status := (ScanAPI{Discovery: testScanDiscovery(t, backend, "service", nil)}).Lookup(context.Background(), "board")
	if status != 503 {
		t.Fatal("missing operation guard allowed discovery")
	}
}

// TestDisabledDeviceCannotReadOffers 用 t 验证暂停设备在注册报价路由上被拒绝；运行态来自窄 fixture，不执行支付或真实设备查询。
func TestDisabledDeviceCannotReadOffers(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"port","device_id":"board","port":{"port_id":"board:1","device_id":"board","port_no":1,"port_status":"idle","online":true,"available":true}}}`))
	}))
	defer backend.Close()
	router := httpapi.NewRouter()
	StartAPI{Checkout: payment.CheckoutService{Ports: testScanDiscovery(t, backend, "service", scanOperation("disabled"))}}.Register(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/user/scan/offers", bytes.NewBufferString(`{"port_id":"board:1"}`)))
	if response.Code != 409 {
		t.Fatalf("disabled offers: %d %s", response.Code, response.Body.String())
	}
}

// TestCanonicalScanCode 用 t 验证裸码及 HTTPS 二维码链接的纯文本解析边界；不访问网络或数据库。
func TestCanonicalScanCode(t *testing.T) {
	for _, tc := range []struct {
		// raw,code 原始二维码文本与预期解析编码。
		raw, code string
		// valid 预期解析结果是否合法。
		valid bool
	}{
		{"board:1", "board:1", true},
		{"https://charge.example/scan?code=board%3A1", "board:1", true},
		{"https://charge.example/scan?code=board:1&code=other", "", false},
		{"http://charge.example/scan?code=board:1", "", false},
		{"https://charge.example/scan?port=board:1", "", false},
	} {
		got, ok := canonicalScanCode(tc.raw)
		if got != tc.code || ok != tc.valid {
			t.Fatalf("scan code %q: code=%q valid=%t", tc.raw, got, ok)
		}
	}
}

// testScanDiscovery 用 backend 的测试 HTTP 服务、token 服务令牌及 operations 窄运行态能力装配具名客户端。
// 返回设备发现用例，t 报告装配错误；不访问真库或真实 gateway，实际 HTTP 请求在处理器执行时发生。
func testScanDiscovery(t *testing.T, backend *httptest.Server, token string, operations device.DeviceOperationReader) device.DiscoveryService {
	t.Helper()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: backend.Client()}, backend.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	return device.DiscoveryService{Gateway: client, Operations: operations}
}
