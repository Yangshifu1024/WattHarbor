package device

import (
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"github.com/gin-gonic/gin"
)

// API 是设备接口的处理器。装配来自 admin.Deps，族内不感知自己被哪个
// 进程挂载：admin 与 central 各自注册自己需要的族。
type API struct{ admin.Deps }

// Register 把设备的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	a.registerImports(r)
	a.registerMeterReviews(r)
	r.GET("/api/v1/admin/devices", a.Auth.Require("device.read"), a.devices)
	r.GET("/api/v1/admin/devices/:id", a.Auth.Require("device.read"), a.device)
	r.PUT("/api/v1/admin/devices/:id", a.Auth.Require("device.operate"), a.updateDevice)
	r.PUT("/api/v1/admin/devices/:id/status", a.Auth.Require("device.operate"), a.setDeviceStatus)
}
