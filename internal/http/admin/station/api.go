package station

import (
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API 持有本业务族所需的数据库、鉴权与外部调用依赖。
type API struct {
	Stations device.StationService // 站点范围读取、资料变更与同事务审计，由 cmd 装配
	DB       *gorm.DB              // 当前范围读取连接
	Auth     authhttp.AdminAuth    // 实时后台会话与权限鉴权
}

// Register 在 r 注册站点路由与相应实时权限中间件；无返回值，不执行业务或启动任务。
func (a API) Register(r *gin.Engine) {
	r.GET("/api/v1/admin/stations", a.Auth.Require("station.read"), a.stations)
	r.GET("/api/v1/admin/stations/:id", a.Auth.Require("station.read"), a.station)
	r.POST("/api/v1/admin/stations", a.Auth.Require("station.create"), a.createStation)
	r.PUT("/api/v1/admin/stations/:id", a.Auth.Require("station.update"), a.updateStation)
}
