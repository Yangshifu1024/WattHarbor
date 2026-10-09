// Package charging 验证当前用户身份并向网关提交停机请求。
package charging

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// StopAPI 适配已认证用户的停止请求，依赖由 cmd 注入；受理及设备控制由 gateway 执行。
type StopAPI struct {
	// JWT 验证用户访问令牌的签名、类型和有效期。
	JWT *auth.JWT
	// Sessions 读取当前会话是否存在，失败沿用会话错误映射。
	Sessions interface {
		// Exists 在 context 下读取 SID 对应会话是否存在，返回布尔值或依赖错误；不修改会话。
		Exists(context.Context, string) (bool, error)
	}
	// Users 读取当前用户是否启用，失败沿用用户错误映射。
	Users interface {
		// Active 在 context 下读取用户编号对应账号是否启用，返回布尔值或依赖错误；不修改账号。
		Active(context.Context, uint64) (bool, error)
	}
	// Gateway 提交停止请求并返回受理回执；不代表设备已停电。
	Gateway interface {
		// RequestStop 在 context 下提交订单、用户及来源，返回受理回执或 HTTP、业务及传输错误；可能投递设备停止命令。
		RequestStop(context.Context, gatewaycontract.StopRequest) (gatewaycontract.StopReply, error)
	}
}

// Register 向 router 注册用户停止端点，只注册路由；鉴权和 gateway 调用在请求处理中执行。
func (a StopAPI) Register(router *gin.Engine) { router.POST("/api/v1/user/charge/stop", a.handle) }

// handle 从 c 验证用户 JWT、会话和状态，再绑定订单号并在请求 context 下提交 gateway 停止请求。
// 成功写受理回执；输入、身份及状态错误保持 400／401／403，订单冲突为 409、无效或拒绝回执为 502、依赖故障为 503；受理不表示设备已停电。
func (a StopAPI) handle(c *gin.Context) {
	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "access token invalid", nil)
		return
	}
	claims, err := a.JWT.Verify(parts[1], "user", time.Now())
	if err != nil || claims.SessionID == "" {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "access token invalid", nil)
		return
	}
	valid, err := a.Sessions.Exists(c.Request.Context(), claims.SessionID)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "session unavailable", nil)
		return
	}
	if !valid {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "session expired", nil)
		return
	}
	userID, err := strconv.ParseUint(claims.Subject, 10, 64)
	if err != nil || userID == 0 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "access token invalid", nil)
		return
	}
	active, err := a.Users.Active(c.Request.Context(), userID)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "user unavailable", nil)
		return
	}
	if !active {
		httpapi.Write(c, http.StatusForbidden, 1003, "account frozen", nil)
		return
	}
	var body struct {
		// OrderNo 请求中的业务订单号，由 gateway 核对归属与可停止状态。
		OrderNo string `json:"order_no" binding:"required,max=64"`
	}
	if c.ShouldBindJSON(&body) != nil {
		httpapi.BadRequest(c, "invalid order number")
		return
	}
	if a.Gateway == nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "gateway configuration invalid", nil)
		return
	}
	acceptance, err := a.Gateway.RequestStop(c.Request.Context(), gatewaycontract.StopRequest{OrderNo: body.OrderNo, UserID: userID, Source: gatewaycontract.StopSourceUser})
	var rejection *serviceclient.HTTPError
	if errors.As(err, &rejection) && rejection.StatusCode == http.StatusConflict {
		httpapi.Write(c, http.StatusConflict, 2000, "order is not stoppable", nil)
		return
	}
	if errors.Is(err, gatewaycontract.ErrInvalidResponse) {
		httpapi.Write(c, http.StatusBadGateway, 5003, "invalid gateway response", nil)
		return
	}
	var businessError gatewaycontract.BusinessError
	if rejection != nil || errors.As(err, &businessError) {
		httpapi.Write(c, http.StatusBadGateway, 5003, "gateway stop not accepted", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "gateway unavailable", nil)
		return
	}
	httpapi.OK(c, acceptance)
}
