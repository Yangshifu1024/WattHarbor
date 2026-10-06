package central

import (
	"context"
	"net/http"
	"time"

	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// AutoStopAPI 适配worker的停机扫描意图，判定、计费截止点冻结与停机下发由所属用例执行。
type AutoStopAPI struct {
	// Stopper 是受请求生命周期管理的所属停机扫描能力。
	Stopper interface {
		// Run 在有界context下执行原停机扫描，返回处理数量或未完成错误，停止命令及截止点由实现管理。
		Run(context.Context) (int, error)
	}
	ServiceToken string // 原内部服务令牌，仅HTTP准入使用，不进入业务用例。
}

// Register 在r挂载原自动停机POST路径，无返回值或后台任务。
func (a AutoStopAPI) Register(r *gin.Engine) {
	r.POST(centralcontract.AutoStopPath, a.autoStop)
}

// autoStop 核对c服务令牌后，以请求派生的25秒context执行一轮所属停机扫描。
// 返回原stopped数量，未完成映射503并保留已处理数量；取消和重试由所属受管理流程负责。
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
