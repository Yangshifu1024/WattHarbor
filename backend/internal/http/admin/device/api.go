package device

import (
	"chargepilot/internal/device"
	adminhttp "chargepilot/internal/http/admin"
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/settlement"
	"github.com/gin-gonic/gin"
)

// API 注入本业务族所需的所属服务、窄查询能力和实时鉴权，不持有数据库连接。
type API struct {
	Management device.ManagementService      // 设备范围读取、运营变更及运行摘要，由 cmd 装配
	Imports    device.ImportService          // 批量导入受理、同步执行及原身份恢复，由 cmd 装配
	Meters     settlement.MeterReviewService // 计量核实、最终金额裁决及审计用例，由 cmd 装配
	Scopes     adminhttp.ScopeReader         // 当前已鉴权账号范围的窄读取能力，由cmd注入。
	Auth       authhttp.AdminAuth            // 实时后台会话及权限
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
