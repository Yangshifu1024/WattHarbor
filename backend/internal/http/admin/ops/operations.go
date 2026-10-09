package ops

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/http/admin"
	"chargepilot/internal/ops"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// registerOperations 在 r 注册运营路由；服务执行公告、账号和设备事务，HTTP 只适配输入与结果。
func (a API) registerOperations(r *gin.Engine) {
	r.GET("/api/v1/admin/alerts", a.Auth.Require("alert.read"), a.listAlerts)
	r.GET("/api/v1/admin/announcements", a.Auth.Require("announcement.read"), a.listAnnouncements)
	r.POST("/api/v1/admin/admin-users", a.Auth.Require("admin_user.create"), a.createUser)
	r.POST("/api/v1/admin/alerts/:id/ack", a.Auth.Require("alert.ack"), a.ackAlert)
	r.POST("/api/v1/admin/announcements", a.Auth.Require("announcement.create"), a.createAnnouncement)
}

// createUser 从请求严格读取账号输入及审计信息，输出创建事务的 ID，不计算散列或校验角色集合。
func (a API) createUser(c *gin.Context) {
	var input ops.AccountCreateInput
	if !httpapi.DecodeResource(c, &input) {
		return
	}
	id, err := a.Accounts.Create(c.Request.Context(), admin.AdminAuditContext(c), input)
	if err != nil {
		admin.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// listAlerts 将当前账号身份传给设备范围查询，输出归一化 items，无 SQL 或告警事务。
func (a API) listAlerts(c *gin.Context) {
	rows, err := a.Alerts.List(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile))
	if err != nil {
		admin.AdminResourceFailure(c, err)
		return
	}
	admin.NormalizeRows(rows)
	httpapi.OK(c, gin.H{"items": rows})
}

// ackAlert 将当前账号身份、路径和审计值传给设备认领事务，输出 acknowledged，不写表。
func (a API) ackAlert(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	if err := a.Alerts.Acknowledge(c.Request.Context(), admin.AdminAuditContext(c), id); err != nil {
		admin.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"acknowledged": true})
}

// listAnnouncements 将当前账号身份交给公告列表，返回 items 或鉴权或存储错误，无 SQL。
func (a API) listAnnouncements(c *gin.Context) {
	rows, err := a.Announcements.List(c.Request.Context(), admin.AdminAuditContext(c).Actor)
	if err != nil {
		admin.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// createAnnouncement 从请求严格读取发布字段及审计值，返回用例事务的 ID／published 或错误。
func (a API) createAnnouncement(c *gin.Context) {
	var in ops.AnnouncementInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	id, err := a.Announcements.Create(c.Request.Context(), admin.AdminAuditContext(c), in)
	if err != nil {
		admin.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "status": "published"})
}
