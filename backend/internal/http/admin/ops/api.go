package ops

import (
	"chargepilot/internal/device"
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/ops"
	"github.com/gin-gonic/gin"
)

// API 适配后台账号创建、公告、审计和告警，业务流程与事务由注入服务拥有。
type API struct {
	Accounts      ops.AccountService      // 所属账号创建完整事务
	Alerts        device.AlertService     // 所属告警查询与认领完整事务
	Announcements ops.AnnouncementService // 公告发布和读取完整入口
	Audits        ops.AuditQueryService   // 审计分页与实时准入
	Auth          authhttp.AdminAuth      // 原后台请求实时授权适配，不承担业务事务
}

// Register 把运营的全部路由挂到引擎上。
func (a API) Register(r *gin.Engine) {
	a.registerOperations(r)
	a.registerAuditLogs(r)
}
