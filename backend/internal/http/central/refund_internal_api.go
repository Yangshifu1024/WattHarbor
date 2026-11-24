package central

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	centralcontract "wattharbor/internal/contracts/central"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
	"wattharbor/internal/settlement"
)

// RefundAPI 接收 worker 的退款请求，调用退款服务执行一批任务。
type RefundAPI struct {
	Executor     settlement.RefundExecutor // 渠道退款批次用例，由 cmd 装配并管理资源。
	ServiceToken string                    // 内部服务令牌，仅 HTTP 鉴权使用，不进入业务用例。
}

// Register 注册内部退款路由，每批最多执行 20 秒。
// 渠道未配置时返回 enabled=false；失败时仍返回已处理数量。
func (a RefundAPI) Register(r *gin.Engine) {
	r.POST(centralcontract.RefundDispatchPath, func(c *gin.Context) {
		if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
			httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
			return
		}
		if a.Executor.Provider == nil {
			enabled := false
			httpapi.OK(c, centralcontract.RefundDispatchReply{Processed: 0, Enabled: &enabled})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		n, err := a.Executor.Batch(ctx)
		if err != nil {
			httpapi.Write(c, 503, 5003, "refund dispatch incomplete; retry scheduled", centralcontract.RefundDispatchReply{Processed: n})
			return
		}
		enabled := true
		httpapi.OK(c, centralcontract.RefundDispatchReply{Processed: n, Enabled: &enabled})
	})
}
