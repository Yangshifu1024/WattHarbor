// Package vendor 提供后台厂商列表和资料管理接口。
package vendor

import (
	"chargepilot/internal/device"
	adminhttp "chargepilot/internal/http/admin"
	authhttp "chargepilot/internal/http/auth"
	"github.com/gin-gonic/gin"
)

// API 提供厂商查询和管理接口，依赖厂商服务和后台鉴权。
type API struct {
	Vendors device.VendorService  // 厂商范围、专用字典操作和提交后审计用例，由 cmd 装配
	Scopes  adminhttp.ScopeReader // 当前已鉴权账号范围的窄读取能力，由 cmd 注入。
	Auth    authhttp.AdminAuth    // 实时会话与权限鉴权
}

// Register 注册厂商查询和管理接口。
func (a API) Register(r *gin.Engine) {
	a.registerVendors(r)
}
