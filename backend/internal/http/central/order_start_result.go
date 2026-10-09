package central

import (
	chargestart "chargepilot/internal/charging/start"
	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
)

// StartResultAPI 接收 worker 提交的启动回执，交给启动服务处理。
type StartResultAPI struct {
	// Service 保存启动回执并更新订单。
	Service interface {
		// Apply 保存启动结果；重复请求返回已处理标记，内容冲突时报错。
		Apply(context.Context, chargestart.StartResult) (bool, error)
	}
	ServiceToken string // 内部服务令牌，仅 HTTP 鉴权使用，不进入业务用例。
}

// Register 注册内部启动回执提交路由。
func (a StartResultAPI) Register(router *gin.Engine) {
	router.POST(centralcontract.StartResultRoute, a.handle)
}

// handle 核对路径与请求中的订单号一致后，提交启动结果。
func (a StartResultAPI) handle(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var request centralcontract.StartResultRequest
	if err := c.ShouldBindJSON(&request); err != nil || c.Param("order_no") != request.OrderNo {
		httpapi.BadRequest(c, "invalid start result")
		return
	}
	replayed, err := a.Service.Apply(c.Request.Context(), chargestart.StartResult(request))
	if errors.Is(err, chargestart.ErrStartResultConflict) {
		httpapi.Write(c, http.StatusConflict, 2000, "start result conflicts with order", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "start result persistence failed", nil)
		return
	}
	httpapi.OK(c, centralcontract.ResultReply{Accepted: true, Replayed: replayed})
}
