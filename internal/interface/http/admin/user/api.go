package user

import (
	"github.com/ChargePilot2026/charge-pilot/internal/card"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API 持有本业务族所需的数据库、鉴权与外部调用依赖。
type API struct {
	Cards card.CardAdminService // 在线卡管理用例，由 cmd 注入
	DB    *gorm.DB
	Auth  chargehttp.AdminAuth
}

// Register 把用户与在线卡的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	a.registerChargeUsers(r)
	a.registerOnlineCards(r)
}
