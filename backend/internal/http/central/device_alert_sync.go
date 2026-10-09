package central

import (
	"net/http"

	contract "chargepilot/internal/contracts/central"
	"chargepilot/internal/device"
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// DeviceAlertSyncAPI 适配 worker 按每秒节奏触发设备告警同步，同步与持久写入由 device 用例拥有。
type DeviceAlertSyncAPI struct {
	Sync         device.AlertSyncService // central 同步用例
	ServiceToken string                  // 内部鉴权凭据，不返回用户
}

// Register 在 r 挂载原告警同步 POST 路径及服务令牌中间件，只注册路由。
func (a DeviceAlertSyncAPI) Register(r *gin.Engine) {
	r.POST(contract.DeviceAlertSyncPath, authhttp.RequireServiceToken(a.ServiceToken), a.sync)
}

// sync 在 c 请求 context 下执行告警同步，成功返回 raised 数量，未完成 503 保留该数量。
// 错误只映射公开提示，HTTP 不持有同步事务或复制告警规则。
func (a DeviceAlertSyncAPI) sync(c *gin.Context) {
	raised, err := a.Sync.Run(c.Request.Context())
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "device alert sync incomplete; retry scheduled", contract.DeviceAlertSyncReply{Raised: raised})
		return
	}
	httpapi.OK(c, contract.DeviceAlertSyncReply{Raised: raised})
}
