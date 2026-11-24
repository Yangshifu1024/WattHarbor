package control

import (
	gatewayrepo "chargepilot/internal/gateway/repo"
	"chargepilot/internal/gateway/session"
	"net/http"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// DeviceEventAPI 提供设备事件、计量证据与订单采样的内部接口。
type DeviceEventAPI struct {
	Snapshots *gatewayrepo.ProcessSnapshots
	// DB 是网关数据库连接。
	DB *gorm.DB
	// Devices 与启动、停止及 ACK 持久化共用，用于串行化端口释放。
	Devices *session.Registry
	// ServiceToken 服务鉴权期望令牌，空配置拒绝调用。
	ServiceToken string
}

// Register 在 router 注册路由；调用方须提供非 nil router。
func (a DeviceEventAPI) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/devices/:device_id/charging-samples", a.chargingSamples)
	// 批量计量查询挂在 /internal 根下：/devices 下已存在 :device_id 通配，
	// 静态段 charging-evidence 会与其在 gin 基数树上冲突。
	router.POST(gatewaycontract.ChargingEvidencePath, a.chargingEvidence)
	router.GET(gatewaycontract.OrderProcessPath+":order_no/process", a.chargeProcess)
	router.GET(gatewaycontract.OrderProcessPath+":order_no/curve", a.orderCurve)
	a.registerDeviceFaults(router)
	a.registerWorkerSync(router)
}

// authorized 精确验证服务请求头；成功返回 true，失败写 401 并返回 false，调用方据此退出。
func (a DeviceEventAPI) authorized(c *gin.Context) bool {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return false
	}
	return true
}
