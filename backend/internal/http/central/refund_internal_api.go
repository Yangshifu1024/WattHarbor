package central

import (
	"context"
	"net/http"
	"time"

	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/settlement"
	"github.com/gin-gonic/gin"
)

// RefundAPI 适配worker的退款派发触发，资金及渠道执行由所属退款用例拥有。
type RefundAPI struct {
	Executor     settlement.RefundExecutor // 原渠道退款批次用例，由cmd装配并管理资源。
	ServiceToken string                    // 原内部服务令牌，仅HTTP准入使用，不进入业务用例。
}

// Register 在r挂载原退款派发POST路径，无返回值或注册期IO。
// 处理器先校验服务令牌；未配置渠道返回enabled=false，否则以请求派生的20秒context执行批次。
// 批次成功返回原processed／enabled，失败503保留已处理数量，不在HTTP重建资金事务或更换请求身份。
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
