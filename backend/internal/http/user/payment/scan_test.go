package payment

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"wattharbor/internal/device"
	"wattharbor/internal/domain/devices"
	gatewayclient "wattharbor/internal/integration/gateway"
	"wattharbor/internal/payment"
	"wattharbor/internal/platform/auth"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
)

// scanSession 是支付路由测试的固定有效会话能力，用于隔离 Redis 会话读取，不创建真实会话。
type scanSession struct{}

// Exists 忽略输入 context 和 SID，返回 true、nil，以隔离支付报价测试会话依赖；不访问 Redis。
func (scanSession) Exists(context.Context, string) (bool, error) { return true, nil }

// scanUser 是支付路由测试的固定有效用户能力，用于隔离账号状态读取，不查询真实用户。
type scanUser struct{}

// Active 忽略输入 context 和用户编号，返回 true、nil，以隔离支付报价测试用户依赖；不访问数据库。
func (scanUser) Active(context.Context, uint64) (bool, error) { return true, nil }

// scanOperation 以字符串指定设备运营状态或读取错误，用于扫码时设备是否可用的判断；不替代网关返回的物理端口状态。
type scanOperation string

// DeviceStatus 忽略 context 和设备编码，返回 s 指定的运营状态或 error 对应读取错误；不访问数据库，仅验证设备发现的校验结果。
func (s scanOperation) DeviceStatus(context.Context, string) (devices.DeviceMetaStatus, error) {
	if s == "error" {
		return "", errors.New("storage unavailable")
	}
	return devices.DeviceMetaStatus(s), nil
}

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

func TestCanonicalScanCode(t *testing.T) {
	for _, tc := range []struct {
		// raw,code 原始二维码文本与预期解析编码。
		raw, code string
		valid     bool
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

// testScanDiscovery 用 backend 的测试 HTTP 服务、token 服务令牌及 operations 运行状态接口装配专用客户端。
// 返回设备发现用例，t 报告装配错误；不访问真库或真实 gateway，实际 HTTP 请求在处理器执行时发生。
func testScanDiscovery(t *testing.T, backend *httptest.Server, token string, operations device.DeviceOperationReader) device.DiscoveryService {
	t.Helper()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: backend.Client()}, backend.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	return device.DiscoveryService{Gateway: client, Operations: operations}
}
