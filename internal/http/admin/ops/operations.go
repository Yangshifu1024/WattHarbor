package ops

import (
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// registerOperations 在r注册原运营路由；所属服务执行公告、账号和设备事务，HTTP只适配输入与结果。
func (a API) registerOperations(r *gin.Engine) {
	r.GET("/api/v1/admin/alerts", a.Auth.Require("alert.read"), a.listAlerts)
	r.GET("/api/v1/admin/announcements", a.Auth.Require("announcement.read"), a.listAnnouncements)
	r.POST("/api/v1/admin/admin-users", a.Auth.Require("admin_user.create"), a.createUser)
	r.POST("/api/v1/admin/alerts/:id/ack", a.Auth.Require("alert.ack"), a.ackAlert)
	r.POST("/api/v1/admin/announcements", a.Auth.Require("announcement.create"), a.createAnnouncement)
}

// createUser 从c严格提取原账号输入及审计事实，输出所属创建事务的ID，不计算散列或校验角色集合。
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

// listAlerts 将c原当前身份传给设备所属范围查询，输出原归一化items，无SQL或告警事务。
func (a API) listAlerts(c *gin.Context) {
	rows, err := a.Alerts.List(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile))
	if err != nil {
		admin.AdminResourceFailure(c, err)
		return
	}
	admin.NormalizeRows(rows)
	httpapi.OK(c, gin.H{"items": rows})
}

// ackAlert 将c原身份、路径和审计值传给设备所属认领事务，输出原acknowledged，不写表。
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

// listAnnouncements 将c当前身份交给所属公告列表，返回原items或准入／存储错误，无SQL。
func (a API) listAnnouncements(c *gin.Context) {
	rows, err := a.Announcements.List(c.Request.Context(), admin.AdminAuditContext(c).Actor)
	if err != nil {
		admin.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// createAnnouncement 从c严格读取原发布字段及审计值，返回所属完整事务的原ID／published或错误。
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
