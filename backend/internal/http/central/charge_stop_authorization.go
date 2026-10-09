package central

import (
	chargestop "chargepilot/internal/charging/stop"
	centralcontract "chargepilot/internal/contracts/central"
	"context"
	"errors"
	"net/http"
	"strconv"

	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// StopAuthorization 适配网关读取本用户停机命令编号，授权与计费截止点由停机用例负责。
type StopAuthorization struct {
	// Service 是 cmd 注入的停机授权接口，不暴露数据库或通用 HTTP。
	Service interface {
		// Stop 以请求 context、订单号及用户 ID 核对归属并冻结截止点，返回停机结果或状态／持久错误。
		Stop(context.Context, string, uint64) (chargestop.ActiveStopAuthorization, error)
	}
	ServiceToken string // 内部服务令牌，仅 HTTP 鉴权使用，不进入业务用例。
}

// Register 将停机授权 GET 路径挂到 router，只注册路由。
func (a StopAuthorization) Register(router *gin.Engine) {
	router.GET(centralcontract.StopAuthorizationRoute, a.handle)
}

// handle 从 c 核对服务令牌、订单号及正整数 user_id，传请求 context 执行停止授权。
// 返回端口／启动命令标识；输入 400、状态或端口 409、截止点及存储 503，不在 HTTP 组合事务。
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
