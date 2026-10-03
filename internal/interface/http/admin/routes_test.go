package admin_test

// 本文件是全应用的路由契约检查：它断言"哪些后台路由已经下线"，
// 因此必须把每个业务族都挂上。拆包前由单一 ResourceAPI 承担这份"全都注册"
// 的角色，现在由本文件显式列出，漏掉哪一族会以 404 暴露出来。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/device"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/finance"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/marketing"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/order"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/station"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/user"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/vendor"
	"github.com/gin-gonic/gin"
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
	for _, endpoint := range []struct{ method, path string }{
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
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(chargehttp.OpenAPIDocument, &document); err != nil {
		t.Fatal(err)
	}
	for path := range document.Paths {
		if strings.Contains(path, "customer-service") || strings.Contains(path, "alert-rules") || strings.Contains(path, "alert-subscriptions") || strings.Contains(path, "/ota/") {
			t.Fatalf("retired route still documented: %s", path)
		}
	}
}
