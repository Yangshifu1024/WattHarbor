package admin_test

// 本文件显式注册全部后台业务族，核对退休与保留端点的路由契约。
// 保留端点应由鉴权返回 401，避免漏注册被 404 掩盖。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"wattharbor/internal/http/admin/device"
	"wattharbor/internal/http/admin/finance"
	"wattharbor/internal/http/admin/marketing"
	"wattharbor/internal/http/admin/ops"
	"wattharbor/internal/http/admin/order"
	"wattharbor/internal/http/admin/pricing"
	"wattharbor/internal/http/admin/station"
	"wattharbor/internal/http/admin/user"
	"wattharbor/internal/http/admin/vendor"
	authhttp "wattharbor/internal/http/auth"
)

func TestRetiredAdminRoutesAreRemoved(t *testing.T) {
	router := gin.New()
	ops.API{}.Register(router)
	pricing.API{}.Register(router)
	station.API{}.Register(router)
	device.API{}.Register(router)
	order.API{}.Register(router)
	finance.API{}.Register(router)
	marketing.API{}.Register(router)
	user.API{}.Register(router)
	vendor.API{}.Register(router)
	for _, endpoint := range []struct {
		// method、path 共同标识应已下线的后台端点。
		method, path string
	}{
		{http.MethodPut, "/api/v1/admin/risk-config"},
		{http.MethodGet, "/api/v1/admin/customer-service"},
		{http.MethodPost, "/api/v1/admin/customer-service"},
		{http.MethodPut, "/api/v1/admin/customer-service/1"},
		{http.MethodDelete, "/api/v1/admin/customer-service/1"},
		{http.MethodGet, "/api/v1/admin/alert-rules"},
		{http.MethodPost, "/api/v1/admin/alert-rules"},
		{http.MethodPut, "/api/v1/admin/alert-rules/1"},
		{http.MethodDelete, "/api/v1/admin/alert-rules/1"},
		{http.MethodPost, "/api/v1/admin/alert-rules/1/resolve"},
		{http.MethodGet, "/api/v1/admin/alert-subscriptions"},
		{http.MethodPost, "/api/v1/admin/alert-subscriptions"},
		{http.MethodDelete, "/api/v1/admin/alert-subscriptions/1"},
		{http.MethodGet, "/api/v1/admin/ota/packages"},
		{http.MethodPost, "/api/v1/admin/ota/packages"},
		{http.MethodDelete, "/api/v1/admin/ota/packages/1"},
		{http.MethodGet, "/api/v1/admin/ota/schedules"},
		{http.MethodPost, "/api/v1/admin/ota/schedules"},
		{http.MethodPost, "/api/v1/admin/ota/schedules/1/trigger"},
		{http.MethodPost, "/api/v1/admin/ota/schedules/1/cancel"},
	} {
		t.Run(endpoint.method+" "+endpoint.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(endpoint.method, endpoint.path, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("retired route returned %d: %s", response.Code, response.Body.String())
			}
		})
	}
	for _, path := range []string{"feedback", "device-fault-reports", "billing/wallet-risks", "alerts"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/"+path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("retained route %s returned %d", path, response.Code)
		}
	}
	for _, path := range []string{"alerts/1/ack"} {
		method := http.MethodPut
		if path == "alerts/1/ack" {
			method = http.MethodPost
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(method, "/api/v1/admin/"+path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("retained route %s returned %d", path, response.Code)
		}
	}
	var document struct {
		// Paths 是嵌入 OpenAPI 的公开路径索引。
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(authhttp.OpenAPIDocument, &document); err != nil {
		t.Fatal(err)
	}
	for path := range document.Paths {
		if strings.Contains(path, "customer-service") || strings.Contains(path, "alert-rules") || strings.Contains(path, "alert-subscriptions") || strings.Contains(path, "/ota/") {
			t.Fatalf("retired route still documented: %s", path)
		}
	}
}
