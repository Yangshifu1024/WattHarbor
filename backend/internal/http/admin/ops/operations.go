package ops

import (
	"github.com/gin-gonic/gin"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/http/admin"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/httpapi"
)

// registerOperations 注册账号创建、告警查询和认领、公告查询和发布接口。
func (a API) registerOperations(r *gin.Engine) {
	r.GET("/api/v1/admin/alerts", a.Auth.Require("alert.read"), a.listAlerts)
	r.GET("/api/v1/admin/announcements", a.Auth.Require("announcement.read"), a.listAnnouncements)
	r.POST("/api/v1/admin/admin-users", a.Auth.Require("admin_user.create"), a.createUser)
	r.POST("/api/v1/admin/alerts/:id/ack", a.Auth.Require("alert.ack"), a.ackAlert)
	r.POST("/api/v1/admin/announcements", a.Auth.Require("announcement.create"), a.createAnnouncement)
}

// createUser 创建后台账号并返回 ID。
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

// listAlerts 查询当前管理员设备范围内的告警。
func (a API) listAlerts(c *gin.Context) {
	rows, err := a.Alerts.List(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile))
	if err != nil {
		admin.AdminResourceFailure(c, err)
		return
	}
	admin.NormalizeRows(rows)
	httpapi.OK(c, gin.H{"items": rows})
}

// ackAlert 认领指定告警，成功返回 acknowledged=true。
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

// listAnnouncements 返回当前管理员可读取的公告列表。
func (a API) listAnnouncements(c *gin.Context) {
	rows, err := a.Announcements.List(c.Request.Context(), admin.AdminAuditContext(c).Actor)
	if err != nil {
		admin.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// createAnnouncement 发布公告并返回 ID 和 published 状态。
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
