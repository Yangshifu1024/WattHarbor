package central

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wattharbor/internal/payment"
	"wattharbor/internal/platform/config"
	"wattharbor/internal/platform/httpapi"
)

// TestCentralAssemblyRejectsInvalidServiceIdentityBeforeBusinessIO 验证内部路由和模拟开关保持一致，空配置或错误令牌返回一次 401。
// 所有业务依赖均为空，确保鉴权失败时不会进入业务处理。
func TestCentralAssemblyRejectsInvalidServiceIdentityBeforeBusinessIO(t *testing.T) {
	routes := []struct {
		method string
		path   string
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
					method string
					path   string
				}{}, routes...)
				if mode == "simulation" {
					expected = append(expected, struct {
						method string
						path   string
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
