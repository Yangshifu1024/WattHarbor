package central

import (
	"net/http"

	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/settlement"
	"github.com/gin-gonic/gin"
)

// BillingAPI 接收 worker 的计费请求，调用结算服务计算和提交费用。
type BillingAPI struct {
	Service      settlement.BillingService // 受请求 context 管理的计费及恢复用例
	ServiceToken string                    // 服务间鉴权凭据，不进入业务用例
}

// Register 注册内部计费路由；失败时返回 503 和已完成数量。
func (a BillingAPI) Register(r *gin.Engine) {
	r.POST(centralcontract.BillingDispatchPath, func(c *gin.Context) {
		if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
			httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
			return
		}
		n, err := a.Service.Run(c.Request.Context())
		if err != nil {
			httpapi.Write(c, 503, 5003, "计费任务稍后重试", centralcontract.BillingDispatchReply{Completed: n})
			return
		}
		httpapi.OK(c, centralcontract.BillingDispatchReply{Completed: n})
	})
}
