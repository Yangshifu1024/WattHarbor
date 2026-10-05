package vendor

import (
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// API 持有本业务族所需的数据库、鉴权与外部调用依赖。
type API struct {
	Vendors device.VendorService // 厂商范围、具名字典操作和提交后审计用例，由 cmd 装配
	DB      *gorm.DB             // 当前数据范围连接
	Auth    chargehttp.AdminAuth // 实时会话与权限鉴权
}

// Register 在 r 注册厂商族路由；无返回值，不读取数据库或启动后台任务。
func (a API) Register(r *gin.Engine) {
	a.registerVendors(r)
}
