package central

import (
	chargefinish "chargepilot/internal/charging/finish"
	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
)

// EndResultAPI 适配worker的原结束结果回执，结束状态与幂等写入由所属收尾用例拥有。
type EndResultAPI struct {
	// Service 是cmd注入的结束回执窄能力，不暴露数据库或通用HTTP。
	Service interface {
		// Apply 在请求context下保存原结束结果，返回是否重放或冲突／持久错误，完整事务由实现拥有。
		Apply(context.Context, chargefinish.EndResult) (bool, error)
	}
	ServiceToken string // 原内部服务令牌，仅HTTP准入使用，不进入业务用例。
}

// Register 在router注册原结束回执POST路径，无返回值或业务IO。
func (a EndResultAPI) Register(router *gin.Engine) {
	router.POST(centralcontract.EndResultRoute, a.handle)
}

// handle 校验c服务令牌、JSON及路径订单号一致性，在请求context下交给所属收尾事务。
// 成功返回accepted／replayed，输入400、身份冲突409、持久失败503；不在HTTP执行资金写入。
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
