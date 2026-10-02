package charge

import (
	"context"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// AutoStopAPI 把停机扫描暴露给 worker 触发；判定、计费截止点冻结与停机
// 下发都在 central 完成，worker 只发"该扫一轮了"的意图。
type AutoStopAPI struct {
	Stopper      AutoStopper
	ServiceToken string
}

func (a AutoStopAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/internal/charge-orders/auto-stop", a.autoStop)
}

func (a AutoStopAPI) autoStop(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	stopped, err := a.Stopper.Run(ctx)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "auto stop sweep incomplete; retry scheduled", gin.H{"stopped": stopped})
		return
	}
	httpapi.OK(c, gin.H{"stopped": stopped})
}
