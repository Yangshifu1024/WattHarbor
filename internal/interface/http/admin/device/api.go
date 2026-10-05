package device

import (
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API 持有本业务族所需的数据库、鉴权与外部调用依赖。
type API struct {
	Management device.ManagementService      // 设备范围读取、运营变更及运行摘要，由 cmd 装配
	Imports    device.ImportService          // 批量导入受理、同步执行及原身份恢复，由 cmd 装配
	Meters     settlement.MeterReviewService // 计量核实、最终金额裁决及审计用例，由 cmd 装配
	DB         *gorm.DB                      // 实时数据范围读取连接
	Auth       chargehttp.AdminAuth          // 实时后台会话及权限
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
