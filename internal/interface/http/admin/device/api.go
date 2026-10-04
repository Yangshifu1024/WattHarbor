package device

import (
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API 持有本业务族所需的数据库、鉴权与外部调用依赖。
type API struct {
	DB                       *gorm.DB
	Auth                     chargehttp.AdminAuth
	GatewayURL, ServiceToken string
}

// Register 把设备的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	a.registerImports(r)
	a.registerMeterReviews(r)
	r.GET("/api/v1/admin/devices", a.Auth.Require("device.read"), a.devices)
	r.GET("/api/v1/admin/devices/:id", a.Auth.Require("device.read"), a.device)
	r.PUT("/api/v1/admin/devices/:id", a.Auth.Require("device.operate"), a.updateDevice)
	r.PUT("/api/v1/admin/devices/:id/status", a.Auth.Require("device.operate"), a.setDeviceStatus)
}
