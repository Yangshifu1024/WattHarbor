package account

import (
	"github.com/gin-gonic/gin"
	"wattharbor/internal/domain/operations"
	adminhttp "wattharbor/internal/http/admin"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/httpapi"
)

// Register 注册后台账号查询、修改、删除、解锁、重置密码和 MFA 管理接口。账号创建由 ops 包注册。
func (a AdminAccounts) Register(r *gin.Engine) {
	r.GET("/api/v1/admin/admin-users", a.Auth.Require("admin_user.read"), a.listAdminUsers)
	r.PUT("/api/v1/admin/admin-users/:id", a.Auth.Require("admin_user.update"), a.updateAdminUser)
	r.DELETE("/api/v1/admin/admin-users/:id", a.Auth.Require("admin_user.delete"), a.deleteAdminUser)
	r.POST("/api/v1/admin/admin-users/:id/unlock", a.Auth.Require("admin_user.update"), a.unlockAdminUser)
	r.POST("/api/v1/admin/admin-users/:id/reset-password", a.Auth.Require("admin_user.reset_password"), a.resetAdminPassword)
	r.POST("/api/v1/admin/admin-users/:id/mfa", a.Auth.Require("admin_user.update"), a.adminUserMFA)
}

// listAdminUsers 按分页、关键词和状态查询后台账号。
func (a AdminAccounts) listAdminUsers(c *gin.Context) {
	page, ok := adminhttp.ReadAdminPage(c, "active disabled locked")
	if !ok {
		return
	}
	reply, err := a.Accounts.List(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), page)
	if err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, reply)
}

// updateAdminUser 更新指定账号，返回 sessions_revoked 表示会话是否已撤销。
func (a AdminAccounts) updateAdminUser(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	var input ops.AccountUpdateInput
	if !httpapi.DecodeResource(c, &input) {
		return
	}
	revoked, err := a.Accounts.Update(c.Request.Context(), adminhttp.AdminAuditContext(c), id, input)
	if err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "sessions_revoked": revoked})
}

// deleteAdminUser 软删除指定后台账号，成功返回 deleted=true。
func (a AdminAccounts) deleteAdminUser(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	if err := a.Accounts.Delete(c.Request.Context(), adminhttp.AdminAuditContext(c), id); err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "deleted": true})
}

// unlockAdminUser 解除指定账号的锁定，成功返回 unlocked=true。
func (a AdminAccounts) unlockAdminUser(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	if err := a.Accounts.Unlock(c.Request.Context(), adminhttp.AdminAuditContext(c), id); err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "unlocked": true})
}

// resetAdminPassword 重置指定账号密码并撤销其会话。
func (a AdminAccounts) resetAdminPassword(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	var input struct {
		NewPassword string `json:"new_password"` /* 12–72 字节新口令，仅交给重置 */
	}
	if !httpapi.DecodeResource(c, &input) {
		return
	}
	if err := a.Accounts.ResetPassword(c.Request.Context(), adminhttp.AdminAuditContext(c), id, input.NewPassword); err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "sessions_revoked": true})
}

// adminUserMFA 按 action 登记、确认或关闭指定账号的 MFA；登记时返回密钥和配置 URI。
func (a AdminAccounts) adminUserMFA(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	var input ops.ManagedMFAInput
	if !httpapi.DecodeResource(c, &input) {
		return
	}
	reply, err := a.Accounts.MFA(c.Request.Context(), adminhttp.AdminAuditContext(c), id, input)
	if err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	if input.Action == "enrol" {
		httpapi.OK(c, gin.H{"secret": reply.Secret, "otpauth_uri": reply.URI, "code": reply.Code, "enabled": false})
		return
	}
	httpapi.OK(c, gin.H{"id": reply.ID, "mfa_enabled": reply.Enabled})
}
