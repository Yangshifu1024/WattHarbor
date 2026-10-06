package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
)

// TestCentralAssemblyRejectsInvalidServiceIdentityBeforeBusinessIO 核对t中的真实cmd装配：
// 原内部方法／路径与模拟模式开关不变，空配置或错误令牌先返回唯一401信封。
// 所有业务、SQL、Redis及网关依赖为空；误入业务会失败，不以该测试证明存储或资金语义。
func TestCentralAssemblyRejectsInvalidServiceIdentityBeforeBusinessIO(t *testing.T) {
	routes := []struct {
		method string // 原注册的HTTP方法。
		path   string // 原路由模板，订单号在请求中替换为固定测试身份。
	}{
		{http.MethodGet, "/api/v1/internal/charge-orders/paid-starts"},
		{http.MethodGet, "/api/v1/internal/charge-orders/:order_no/start-authorization"},
		{http.MethodGet, "/api/v1/internal/charge-orders/:order_no/stop-authorization"},
		{http.MethodPost, "/api/v1/internal/charge-orders/:order_no/start-result"},
		{http.MethodPost, "/api/v1/internal/charge-orders/:order_no/end-result"},
		{http.MethodPost, "/api/v1/internal/charge-orders/auto-stop"},
		{http.MethodPost, "/api/v1/internal/cards/swipe"},
		{http.MethodGet, "/api/v1/internal/cards/balance"},
		{http.MethodPost, "/api/v1/internal/billing/dispatch"},
		{http.MethodPost, "/api/v1/internal/refunds/dispatch"},
		{http.MethodPost, "/api/v1/internal/device-alerts/sync"},
	}
	for _, mode := range []string{"disabled", "simulation", "wechat_direct"} {
		for _, configured := range []string{"", "central-assembly-secret"} {
			t.Run(mode+"/configured="+configured, func(t *testing.T) {
				router := newRouter(config.Central{PaymentMode: mode, ServiceToken: configured}, &resources{}, payment.PaymentChannel{}, nil)
				expected := append([]struct {
					method string // 测试副本的原方法。
					path   string // 测试副本的原模板。
				}{}, routes...)
				if mode == "simulation" {
					expected = append(expected, struct {
						method string // 模拟回调方法。
						path   string // 只在模拟模式注册的内部回调路径。
					}{http.MethodPost, "/api/v1/internal/payments/simulate-success"})
				}
				registered := make(map[string]bool)
				for _, route := range router.Routes() {
					if strings.HasPrefix(route.Path, "/api/v1/") {
						registered[route.Method+" "+route.Path] = true
					}
				}
				if len(registered) != len(expected) {
					t.Fatalf("API route count=%d want=%d: %v", len(registered), len(expected), registered)
				}
				for _, route := range expected {
					if !registered[route.method+" "+route.path] {
						t.Fatalf("missing registered route %s %s", route.method, route.path)
					}
					for _, supplied := range []string{"", "wrong-secret"} {
						t.Run(route.method+" "+route.path+"/supplied="+supplied, func(t *testing.T) {
							request := httptest.NewRequest(route.method, strings.ReplaceAll(route.path, ":order_no", "C-assembly"), strings.NewReader("{}"))
							request.Header.Set("Content-Type", "application/json")
							request.Header.Set("X-Request-ID", "central-assembly-check")
							request.Header.Set("X-Service-Token", supplied)
							response := httptest.NewRecorder()
							router.ServeHTTP(response, request)
							decoder := json.NewDecoder(response.Body)
							var envelope httpapi.Envelope
							if err := decoder.Decode(&envelope); err != nil || response.Code != http.StatusUnauthorized || envelope.Code != httpapi.CodeUnauthorized || envelope.Message != "service token invalid" || envelope.RequestID != "central-assembly-check" || envelope.TraceID != envelope.RequestID || envelope.Data != nil {
								t.Fatalf("status=%d envelope=%+v decode=%v", response.Code, envelope, err)
							}
							var extra any
							if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
								t.Fatalf("extra response: value=%v error=%v", extra, err)
							}
						})
					}
				}
			})
		}
	}
}
