package central

import (
	"net/http"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// DeviceAlertSyncAPI 适配worker按原每秒节奏触发设备告警同步，同步与持久写入由device用例拥有。
type DeviceAlertSyncAPI struct {
	Sync         device.AlertSyncService // central所属同步用例
	ServiceToken string                  // 原内部鉴权凭据，不返回用户
}

// Register 在r挂载原告警同步POST路径及服务令牌中间件，无返回值、业务IO或后台任务。
func (a DeviceAlertSyncAPI) Register(r *gin.Engine) {
	r.POST(contract.DeviceAlertSyncPath, authhttp.RequireServiceToken(a.ServiceToken), a.sync)
}

// sync 在c原请求context下执行所属告警同步，成功返回原raised数量，未完成503保留该数量。
// 错误只映射原公开提示，HTTP不持有同步事务或复制告警规则。
func (a DeviceAlertSyncAPI) sync(c *gin.Context) {
	raised, err := a.Sync.Run(c.Request.Context())
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "device alert sync incomplete; retry scheduled", contract.DeviceAlertSyncReply{Raised: raised})
		return
	}
	httpapi.OK(c, contract.DeviceAlertSyncReply{Raised: raised})
}
