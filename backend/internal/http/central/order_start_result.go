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

// StartResultAPI 适配worker的原起充结果回执，状态及幂等写入由所属起充用例拥有。
type StartResultAPI struct {
	// Service 是cmd注入的起充回执窄能力，不暴露数据库或通用HTTP。
	Service interface {
		// Apply 在请求context下保存原起充结果，返回是否重放或冲突／持久错误，完整事务由实现拥有。
		Apply(context.Context, chargestart.StartResult) (bool, error)
	}
	ServiceToken string // 原内部服务令牌，仅HTTP准入使用，不进入业务用例。
}

// Register 在router注册原起充回执POST路径，无返回值或业务IO。
func (a StartResultAPI) Register(router *gin.Engine) {
	router.POST(centralcontract.StartResultRoute, a.handle)
}

// handle 校验c服务令牌、JSON及路径订单号一致性，将原回执交给请求context下的所属事务。
// 成功返回accepted／replayed，输入400、身份冲突409、持久失败503；不自行持有事务。
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
