package marketing

import (
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"github.com/gin-gonic/gin"
)

// API 是优惠券与活动接口的处理器。装配来自 admin.Deps，族内不感知自己被哪个
// 进程挂载：admin 与 central 各自注册自己需要的族。
type API struct{ admin.Deps }

// Register 把优惠券与活动的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	a.registerCoupons(r)
	a.registerActivityRules(r)
}
