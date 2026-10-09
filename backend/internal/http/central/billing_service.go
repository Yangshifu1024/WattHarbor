package central

import (
	"net/http"

	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/settlement"
	"github.com/gin-gonic/gin"
)

// BillingAPI 适配central原计费派发协议，费用写入、账单及恢复由所属settlement用例拥有。
type BillingAPI struct {
	Service      settlement.BillingService // 受请求 context 管理的计费及恢复用例
	ServiceToken string                    // 服务间鉴权凭据，不进入业务用例
}

// Register 在r挂载原内部计费派发POST路径，无返回值、注册期IO或后台任务。
// 处理器先校验服务令牌，再以请求context执行所属计费；成功返回completed，失败503保留已完成数量。
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
