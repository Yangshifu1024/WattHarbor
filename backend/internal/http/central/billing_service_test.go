package central

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestDispatchRejectsInvalidServiceCredentialsBeforeStorage 核对真实注册的计费路由及 401／1001。
// 依赖未装配，空配置、缺凭据及坏凭据必须在业务访问前返回；不验证计费或数据库事务。
func TestDispatchRejectsInvalidServiceCredentialsBeforeStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		configured, supplied string // 路由配置令牌和请求提供令牌，覆盖空配置、缺凭据及坏凭据。
	}{
		{"", ""}, {"billing-test-secret", ""}, {"billing-test-secret", "wrong-secret"},
	} {
		router := gin.New()
		// 未通过鉴权的请求不得访问数据库或订单服务。
		(BillingAPI{ServiceToken: tc.configured}).Register(router)
		req := httptest.NewRequest("POST", "/api/v1/internal/billing/dispatch", nil)
		req.Header.Set("X-Service-Token", tc.supplied)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != 401 || !strings.Contains(response.Body.String(), `"code":1001`) {
			t.Fatalf("unauthorized billing dispatch: %d %s", response.Code, response.Body.String())
		}
	}
}
