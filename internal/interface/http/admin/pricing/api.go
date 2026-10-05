package pricing

import (
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API 持有本业务族所需的数据库、鉴权与外部调用依赖。
type API struct {
	Queries      pricing.QueryService       // 模板、有效方案及协议能力的只读组合，由 cmd 装配
	DB           *gorm.DB                   // 当前只读列表及实时范围加载，由进程管理
	Auth         chargehttp.AdminAuth       // 注册路由的实时会话及权限校验
	Schemes      pricing.SchemeService      // 模板变更及同事务审计入口，由 cmd 注入
	Publications pricing.PublicationService // 目标发布及继承入口，由 cmd 注入
}

// Register 将方案模板、发布、继承、预览及能力路由挂到 r，注册实时权限中间件，不启动后台任务。
func (a API) Register(r *gin.Engine) {
	a.registerChargingSchemes(r)
}
