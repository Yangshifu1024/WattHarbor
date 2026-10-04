package http

import (
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/gin-gonic/gin"
	"net/http"
)

// BillingAPI 只适配 central 的计费派发协议，事务与补出账归 settlement。
type BillingAPI struct {
	Service      settlement.BillingService // 受请求 context 管理的计费及恢复用例
	ServiceToken string                    // 服务间鉴权凭据，不进入业务用例
}

// Register 在 r 注册原内部派发端点，无返回值；不启动后台任务。
// 路由先校验服务身份，再调用用例；失败响应携带本轮已完成数量。
func (a BillingAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/internal/billing/dispatch", func(c *gin.Context) {
		if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
			httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
			return
		}
		n, err := a.Service.Run(c.Request.Context())
		if err != nil {
			httpapi.Write(c, 503, 5003, "计费任务稍后重试", gin.H{"completed": n})
			return
		}
		httpapi.OK(c, gin.H{"completed": n})
	})
}
