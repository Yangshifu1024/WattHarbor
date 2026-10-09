package central

import (
	"net/http"

	contract "chargepilot/internal/contracts/central"
	"chargepilot/internal/device"
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// DeviceAlertSyncAPI 接收 worker 的设备告警同步请求。
type DeviceAlertSyncAPI struct {
	Sync         device.AlertSyncService // central 同步用例
	ServiceToken string                  // 内部鉴权凭据，不返回用户
}

// Register 注册告警同步路由，要求服务令牌。
func (a DeviceAlertSyncAPI) Register(r *gin.Engine) {
	r.POST(contract.DeviceAlertSyncPath, authhttp.RequireServiceToken(a.ServiceToken), a.sync)
}

// sync 执行一轮告警同步；失败时返回 503 和已新增告警数量。
func (a DeviceAlertSyncAPI) sync(c *gin.Context) {
	raised, err := a.Sync.Run(c.Request.Context())
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "device alert sync incomplete; retry scheduled", contract.DeviceAlertSyncReply{Raised: raised})
		return
	}
	httpapi.OK(c, contract.DeviceAlertSyncReply{Raised: raised})
}
