package control

import (
	"net/http/httptest"
	"strings"
	"testing"

	"wattharbor/internal/platform/httpapi"
)

// TestWorkerSyncAPIRequiresServiceToken 通过实际注册的十三条同步路由验证缺失服务令牌返回 401；
// fixture 未注入数据库，拒绝应先于持久操作，本测试不验证合法身份的业务读写。
func TestWorkerSyncAPIRequiresServiceToken(t *testing.T) {
	router := httpapi.NewRouter()
	(DeviceEventAPI{ServiceToken: "service"}).Register(router)
	cases := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/internal/start-results"},
		{"POST", "/api/v1/internal/start-results/mark-reported"},
		{"GET", "/api/v1/internal/end-events"},
		{"GET", "/api/v1/internal/charge-commands/by-order/1"},
		{"POST", "/api/v1/internal/device-events/mark-processed"},
		{"POST", "/api/v1/internal/ports/release"},
		{"GET", "/api/v1/internal/devices/d-1/meter-samples?ack_at=2026-09-29T12:00:00Z&end_at=2026-09-29T12:30:00Z&end_id=1"},
		{"POST", "/api/v1/internal/charge-end-deliveries/freeze"},
		{"GET", "/api/v1/internal/card-events"},
		{"POST", "/api/v1/internal/card-events/decide-begin"},
		{"POST", "/api/v1/internal/card-events/decide-finish"},
		{"POST", "/api/v1/internal/card-events/advance"},
		{"GET", "/api/v1/internal/ports/resolve?device_id=d-1&port_no=1"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		router.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("%s %s status=%d want=401", tc.method, tc.path, w.Code)
		}
	}
}
