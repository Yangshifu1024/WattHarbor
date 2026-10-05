package ops

import (
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/gin-gonic/gin"
)

// API 持有本业务族所需的数据库、鉴权与外部调用依赖。
type API struct {
	Accounts      ops.AccountService      // 所属账号创建完整事务
	Alerts        device.AlertService     // 所属告警查询与认领完整事务
	Announcements ops.AnnouncementService // 公告发布和读取完整入口
	Audits        ops.AuditQueryService   // 审计分页与实时准入
	Auth          chargehttp.AdminAuth
}

// Register 把运营的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	a.registerOperations(r)
	a.registerAuditLogs(r)
}
