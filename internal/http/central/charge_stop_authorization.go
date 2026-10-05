package central

import (
	"context"
	"errors"
	chargestop "github.com/ChargePilot2026/charge-pilot/internal/charging/stop"
	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"net/http"
	"strconv"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// StopAuthorization 适配网关读取本用户原停止身份，授权与计费截止点由所属停机用例负责。
type StopAuthorization struct {
	// Service 是cmd注入的停机授权窄能力，不暴露数据库或通用HTTP。
	Service interface {
		// Stop 以请求context、原订单号及用户ID核对归属并冻结截止点，返回原停机事实或状态／持久错误。
		Stop(context.Context, string, uint64) (chargestop.ActiveStopAuthorization, error)
	}
	ServiceToken string // 原内部服务令牌，仅HTTP准入使用，不进入业务用例。
}

// Register 将原停机授权GET路径挂到router，无返回值或业务IO。
func (a StopAuthorization) Register(router *gin.Engine) {
	router.GET(centralcontract.StopAuthorizationRoute, a.handle)
}

// handle 从c核对服务令牌、订单号及正整数user_id，传请求context执行所属停止授权。
// 返回原端口／启动命令身份；输入400、状态或端口409、截止点及存储503，不在HTTP组合事务。
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
