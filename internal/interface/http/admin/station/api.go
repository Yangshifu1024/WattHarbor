package station

import (
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API 持有本业务族所需的数据库、鉴权与外部调用依赖。
type API struct {
	DB   *gorm.DB
	Auth chargehttp.AdminAuth
}

// Register 把站点的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	r.GET("/api/v1/admin/stations", a.Auth.Require("station.read"), a.stations)
	r.GET("/api/v1/admin/stations/:id", a.Auth.Require("station.read"), a.station)
	r.POST("/api/v1/admin/stations", a.Auth.Require("station.create"), a.createStation)
	r.PUT("/api/v1/admin/stations/:id", a.Auth.Require("station.update"), a.updateStation)
}
