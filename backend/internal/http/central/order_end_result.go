package central

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	chargefinish "wattharbor/internal/charging/finish"
	centralcontract "wattharbor/internal/contracts/central"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
)

// EndResultAPI 接收 worker 提交的设备结束结果，交给结束服务处理。
type EndResultAPI struct {
	// Service 保存结束回执并更新订单。
	Service interface {
		// Apply 保存结束结果；重复请求返回已处理标记，内容冲突时报错。
		Apply(context.Context, chargefinish.EndResult) (bool, error)
	}
	ServiceToken string // 内部服务令牌，仅 HTTP 鉴权使用，不进入业务用例。
}

// Register 注册内部结束回执提交路由。
func (a EndResultAPI) Register(router *gin.Engine) {
	router.POST(centralcontract.EndResultRoute, a.handle)
}

// handle 核对路径与请求中的订单号一致后，提交结束结果。
func (a EndResultAPI) handle(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	var request centralcontract.EndResultRequest
	if c.ShouldBindJSON(&request) != nil || c.Param("order_no") != request.OrderNo {
		httpapi.BadRequest(c, "invalid end result")
		return
	}
	replayed, err := a.Service.Apply(c.Request.Context(), chargefinish.EndResult(request))
	if errors.Is(err, chargefinish.ErrEndResultConflict) {
		httpapi.Write(c, http.StatusConflict, 2000, "end result conflicts with order", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "end result persistence failed", nil)
		return
	}
	httpapi.OK(c, centralcontract.ResultReply{Accepted: true, Replayed: replayed})
}
