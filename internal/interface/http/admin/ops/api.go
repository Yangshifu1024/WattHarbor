package ops

import (
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API 持有本业务族所需的数据库、鉴权与外部调用依赖。
type API struct {
	Alerts device.AlertService // 所属告警查询与认领完整事务
	DB     *gorm.DB
	Auth   chargehttp.AdminAuth
}

// Register 把运营的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	a.registerOperations(r)
	a.registerAuditLogs(r)
}
