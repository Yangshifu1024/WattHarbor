package central

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	centralcontract "wattharbor/internal/contracts/central"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
)

// AutoStopAPI 接收 worker 的自动停机扫描请求。
type AutoStopAPI struct {
	// Stopper 是受请求生命周期管理的停机扫描能力。
	Stopper interface {
		// Run 扫描应停止的订单，保存计费截止时间并请求停机；失败时保留处理数量。
		Run(context.Context) (int, error)
	}
	ServiceToken string // 内部服务令牌，仅 HTTP 鉴权使用，不进入业务用例。
}

// Register 注册自动停机扫描路由。
func (a AutoStopAPI) Register(r *gin.Engine) {
	r.POST(centralcontract.AutoStopPath, a.autoStop)
}

// autoStop 校验服务令牌，并在 25 秒内执行一轮扫描。
// 未完成时返回 503，仍返回已处理数量。
func (a AutoStopAPI) autoStop(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	stopped, err := a.Stopper.Run(ctx)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "auto stop sweep incomplete; retry scheduled", centralcontract.AutoStopReply{Stopped: stopped})
		return
	}
	httpapi.OK(c, centralcontract.AutoStopReply{Stopped: stopped})
}
