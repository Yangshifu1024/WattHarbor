// Package station 提供后台站点资料管理接口。
package station

import (
	"chargepilot/internal/device"
	adminhttp "chargepilot/internal/http/admin"
	authhttp "chargepilot/internal/http/auth"
	"github.com/gin-gonic/gin"
)

// API 提供站点管理接口，依赖站点服务、当前范围读取和后台鉴权。
type API struct {
	Stations device.StationService // 站点范围读取、资料变更与同事务审计，由 cmd 装配
	Scopes   adminhttp.ScopeReader // 当前已鉴权账号范围的窄读取能力，由 cmd 注入。
	Auth     authhttp.AdminAuth    // 实时后台会话与权限鉴权
}

// Register 注册站点查询、创建和修改接口。
func (a API) Register(r *gin.Engine) {
	r.GET("/api/v1/admin/stations", a.Auth.Require("station.read"), a.stations)
	r.GET("/api/v1/admin/stations/:id", a.Auth.Require("station.read"), a.station)
	r.POST("/api/v1/admin/stations", a.Auth.Require("station.create"), a.createStation)
	r.PUT("/api/v1/admin/stations/:id", a.Auth.Require("station.update"), a.updateStation)
}
