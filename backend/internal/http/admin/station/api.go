package station

import (
	"chargepilot/internal/device"
	adminhttp "chargepilot/internal/http/admin"
	authhttp "chargepilot/internal/http/auth"
	"github.com/gin-gonic/gin"
)

// API 注入本业务族所需的所属服务、窄查询能力和实时鉴权，不持有数据库连接。
type API struct {
	Stations device.StationService // 站点范围读取、资料变更与同事务审计，由 cmd 装配
	Scopes   adminhttp.ScopeReader // 当前已鉴权账号范围的窄读取能力，由cmd注入。
	Auth     authhttp.AdminAuth    // 实时后台会话与权限鉴权
}

// Register 在 r 注册站点路由与相应实时权限中间件；无返回值，不执行业务或启动任务。
func (a API) Register(r *gin.Engine) {
	r.GET("/api/v1/admin/stations", a.Auth.Require("station.read"), a.stations)
	r.GET("/api/v1/admin/stations/:id", a.Auth.Require("station.read"), a.station)
	r.POST("/api/v1/admin/stations", a.Auth.Require("station.create"), a.createStation)
	r.PUT("/api/v1/admin/stations/:id", a.Auth.Require("station.update"), a.updateStation)
}
