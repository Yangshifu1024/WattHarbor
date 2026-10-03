package ops

import (
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"github.com/gin-gonic/gin"
)

// API 是运营接口的处理器。装配来自 admin.Deps，族内不感知自己被哪个
// 进程挂载：admin 与 central 各自注册自己需要的族。
type API struct{ admin.Deps }

// Register 把运营的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	a.registerOperations(r)
	a.registerAuditLogs(r)
}
