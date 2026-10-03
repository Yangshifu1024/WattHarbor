package alertsync

import (
	"context"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// DeviceAlertSyncAPI 把设备告警同步暴露给 worker 触发；同步在 central 完成，
// worker 只按原每秒节奏发触发意图，不接触 device_event 或 alert_event。
type DeviceAlertSyncAPI struct {
	Sync         DeviceAlertSync
	ServiceToken string
}

func (a DeviceAlertSyncAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/internal/device-alerts/sync", admin.RequireServiceToken(a.ServiceToken), a.sync)
}

func (a DeviceAlertSyncAPI) sync(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	raised, err := a.Sync.Run(ctx)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "device alert sync incomplete; retry scheduled", gin.H{"raised": raised})
		return
	}
	httpapi.OK(c, gin.H{"raised": raised})
}
