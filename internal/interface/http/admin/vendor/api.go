package vendor

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

// Register 把厂商的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	a.registerVendors(r)
}
