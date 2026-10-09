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

// EndResultAPI 适配 worker 的结束结果回执，结束状态与幂等写入由收尾用例拥有。
type EndResultAPI struct {
	// Service 是 cmd 注入的结束回执接口，不暴露数据库或通用 HTTP。
	Service interface {
		// Apply 在请求 context 下保存结束结果，返回是否重放或冲突／持久错误，完整事务由实现拥有。
		Apply(context.Context, chargefinish.EndResult) (bool, error)
	}
	ServiceToken string // 内部服务令牌，仅 HTTP 鉴权使用，不进入业务用例。
}

// Register 在 router 注册结束回执 POST 路径，只注册路由。
func (a EndResultAPI) Register(router *gin.Engine) {
	router.POST(centralcontract.EndResultRoute, a.handle)
}

// handle 校验 请求中的服务令牌、JSON 及路径订单号一致性，在请求 context 下交给收尾事务。
// 成功返回 accepted／replayed，输入 400、标识冲突 409、持久失败 503；不在 HTTP 执行资金写入。
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
