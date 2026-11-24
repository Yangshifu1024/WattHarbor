package central

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	chargestop "wattharbor/internal/charging/stop"
	centralcontract "wattharbor/internal/contracts/central"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
)

// StopAuthorization 为网关提供停机授权；停机服务会核对用户归属并固定计费截止时间。
type StopAuthorization struct {
	// Service 核对订单并保存计费截止时间。
	Service interface {
		// Stop 核对用户与订单，返回允许停止的订单及启动命令编号。
		Stop(context.Context, string, uint64) (chargestop.ActiveStopAuthorization, error)
	}
	ServiceToken string // 内部服务令牌，仅 HTTP 鉴权使用，不进入业务用例。
}

// Register 注册内部停机授权路由。
func (a StopAuthorization) Register(router *gin.Engine) {
	router.GET(centralcontract.StopAuthorizationRoute, a.handle)
}

// handle 校验令牌、订单号和用户 ID，返回停机授权。
func (a StopAuthorization) handle(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	userID, err := strconv.ParseUint(c.Query("user_id"), 10, 64)
	if err != nil || userID == 0 || c.Param("order_no") == "" {
		httpapi.BadRequest(c, "invalid stop authorization request")
		return
	}
	order, err := a.Service.Stop(c.Request.Context(), c.Param("order_no"), userID)
	if errors.Is(err, chargestop.ErrBillingCutoffUnavailable) {
		httpapi.Write(c, 503, 5001, "计费截止暂时无法保存", nil)
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, http.StatusConflict, 2000, "order is not charging for this user", nil)
		return
	}
	if errors.Is(err, chargestop.ErrPortIdentity) {
		httpapi.Write(c, http.StatusConflict, 2000, "port identity unavailable", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "order storage unavailable", nil)
		return
	}
	httpapi.OK(c, centralcontract.StopAuthorizationReply{ChargeOrderID: order.ChargeOrderID, OrderNo: order.OrderNo,
		UserID: order.UserID, DeviceID: order.DeviceID, PortNo: order.PortNo,
		PortID: uint64(order.PortID.Int64), StartCommandID: order.StartCommandID})
}
