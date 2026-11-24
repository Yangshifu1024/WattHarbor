package central

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	chargestop "wattharbor/internal/charging/stop"
)

// stopAuthorizationStub 仅返回指定的停止授权错误，供真实注册 HTTP 路由核对公开映射。
type stopAuthorizationStub struct {
	err error // 授权状态、端口、截止点或存储错误，不模拟实际事务。
}

// Stop 忽略输入 context、订单号及用户 ID，返回零授权结果和 s.err；无状态写入、IO 或取消模拟。
func (s stopAuthorizationStub) Stop(context.Context, string, uint64) (chargestop.ActiveStopAuthorization, error) {
	return chargestop.ActiveStopAuthorization{}, s.err
}

// TestStopAuthorizationKeepsErrorContract 检查真实注册路由的 409／503 提示及私有原因不外泄。
// 用例为错误 fixture，不验证真实用户归属、截止点冻结、行锁或设备停机。
func TestStopAuthorizationKeepsErrorContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		err     error  // fixture 原样返回的授权或存储错误。
		status  int    // 该错误契约对应的 HTTP 状态。
		message string // 响应应包含的公开提示，不包含私有 SQL 原因。
	}{
		{gorm.ErrRecordNotFound, 409, "order is not charging for this user"},
		{chargestop.ErrPortIdentity, 409, "port identity unavailable"},
		{chargestop.ErrBillingCutoffUnavailable, 503, "计费截止暂时无法保存"},
		{errors.New("private SQL error"), 503, "order storage unavailable"},
	} {
		r := gin.New()
		StopAuthorization{Service: stopAuthorizationStub{err: test.err}, ServiceToken: "secret"}.Register(r)
		req := httptest.NewRequest("GET", "/api/v1/internal/charge-orders/C-1/stop-authorization?user_id=1", nil)
		req.Header.Set("X-Service-Token", "secret")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != test.status || !strings.Contains(w.Body.String(), test.message) || strings.Contains(w.Body.String(), "private SQL error") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
}
