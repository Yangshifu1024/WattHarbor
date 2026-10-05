package alertsync

import (
	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"net/http"

	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// DeviceAlertSyncAPI 把设备告警同步暴露给 worker 触发；同步在 central 完成，
// worker 只按原每秒节奏发触发意图，不接触 device_event 或 alert_event。
type DeviceAlertSyncAPI struct {
	Sync         device.AlertSyncService // central所属同步用例
	ServiceToken string                  // 原内部鉴权凭据，不返回用户
}

// Register 在r上注册原服务鉴权同步端点，无数据库或后台任务。
func (a DeviceAlertSyncAPI) Register(r *gin.Engine) {
	r.POST(contract.DeviceAlertSyncPath, admin.RequireServiceToken(a.ServiceToken), a.sync)
}

// sync 使用c原请求context触发所属用例，输出原raised及失败503，不持有业务事务。
func (a DeviceAlertSyncAPI) sync(c *gin.Context) {
	raised, err := a.Sync.Run(c.Request.Context())
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "device alert sync incomplete; retry scheduled", contract.DeviceAlertSyncData{Raised: raised})
		return
	}
	httpapi.OK(c, contract.DeviceAlertSyncData{Raised: raised})
}
