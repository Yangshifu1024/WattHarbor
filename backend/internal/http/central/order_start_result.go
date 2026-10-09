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

// StartResultAPI 适配 worker 的起充结果回执，状态及幂等写入由起充用例拥有。
type StartResultAPI struct {
	// Service 是 cmd 注入的起充回执接口，不暴露数据库或通用 HTTP。
	Service interface {
		// Apply 在请求 context 下保存起充结果，返回是否重放或冲突／持久错误，完整事务由实现拥有。
		Apply(context.Context, chargestart.StartResult) (bool, error)
	}
	ServiceToken string // 内部服务令牌，仅 HTTP 鉴权使用，不进入业务用例。
}

// Register 在 router 注册起充回执 POST 路径，只注册路由。
func (a StartResultAPI) Register(router *gin.Engine) {
	router.POST(centralcontract.StartResultRoute, a.handle)
}

// handle 校验 请求中的服务令牌、JSON 及路径订单号一致性，将回执交给请求 context 下的调用方事务。
// 成功返回 accepted／replayed，输入 400、标识冲突 409、持久失败 503；不自行持有事务。
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
