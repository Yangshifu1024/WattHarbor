package pricing

import (
	adminhttp "chargepilot/internal/http/admin"
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/pricing"
	"github.com/gin-gonic/gin"
)

// API 注入本业务族所需的所属服务、窄查询能力和实时鉴权，不持有数据库连接。
type API struct {
	Queries      pricing.QueryService       // 模板、有效方案及协议能力的只读组合，由 cmd 装配
	Scopes       adminhttp.ScopeReader      // 当前已鉴权账号范围的窄读取能力，由cmd注入。
	Auth         authhttp.AdminAuth         // 注册路由的实时会话及权限校验
	Schemes      pricing.SchemeService      // 模板变更及同事务审计入口，由 cmd 注入
	Publications pricing.PublicationService // 目标发布及继承入口，由 cmd 注入
}

// Register 将方案模板、发布、继承、预览及能力路由挂到 r，注册实时权限中间件，不启动后台任务。
func (a API) Register(r *gin.Engine) {
	a.registerChargingSchemes(r)
}
