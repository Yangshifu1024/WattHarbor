// Package pricing 提供充电方案编辑、发布、继承和预览接口。
package pricing

import (
	"github.com/gin-gonic/gin"
	adminhttp "wattharbor/internal/http/admin"
	authhttp "wattharbor/internal/http/auth"
	"wattharbor/internal/pricing"
)

// API 提供充电方案接口，依赖方案管理、查询和后台鉴权。
type API struct {
	Queries      pricing.QueryService       // 模板、有效方案及协议能力的只读组合，由 cmd 装配
	Scopes       adminhttp.ScopeReader      // 当前已鉴权账号范围的窄读取能力，由 cmd 注入。
	Auth         authhttp.AdminAuth         // 注册路由的实时会话及权限校验
	Schemes      pricing.SchemeService      // 模板变更及同事务审计入口，由 cmd 注入
	Publications pricing.PublicationService // 目标发布及继承入口，由 cmd 注入
}

// Register 注册方案模板、发布、继承、预览和设备能力接口。
func (a API) Register(r *gin.Engine) {
	a.registerChargingSchemes(r)
}
