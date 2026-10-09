package vendor

import (
	"chargepilot/internal/device"
	adminhttp "chargepilot/internal/http/admin"
	authhttp "chargepilot/internal/http/auth"
	"github.com/gin-gonic/gin"
)

// API 注入本业务族所需的所属服务、窄查询能力和实时鉴权，不持有数据库连接。
type API struct {
	Vendors device.VendorService  // 厂商范围、具名字典操作和提交后审计用例，由 cmd 装配
	Scopes  adminhttp.ScopeReader // 当前已鉴权账号范围的窄读取能力，由cmd注入。
	Auth    authhttp.AdminAuth    // 实时会话与权限鉴权
}

// Register 在 r 注册厂商族路由；无返回值，不读取数据库或启动后台任务。
func (a API) Register(r *gin.Engine) {
	a.registerVendors(r)
}
