package user

import (
	"github.com/ChargePilot2026/charge-pilot/internal/card"
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/identity"
	"github.com/gin-gonic/gin"
)

// API 仅持有用户与卡用例及后台鉴权适配，不直接装配表访问或外部调用。
type API struct {
	Cards card.CardAdminService     // 在线卡管理用例，由 cmd 注入
	Users identity.UserQueryService // 所属用户分页、档案和充值历史用例
	Auth  authhttp.AdminAuth        // 原实时后台身份与权限适配
}

// Register 把用户与在线卡的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	a.registerChargeUsers(r)
	a.registerOnlineCards(r)
}
