package account

import (
	"chargepilot/internal/domain/operations"
	adminhttp "chargepilot/internal/http/admin"
	"chargepilot/internal/ops"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// Register 在 r 挂载账号读取和管理路径，创建路径仍由运营族注册，避免重复注册。
func (a AdminAccounts) Register(r *gin.Engine) {
	r.GET("/api/v1/admin/admin-users", a.Auth.Require("admin_user.read"), a.listAdminUsers)
	r.PUT("/api/v1/admin/admin-users/:id", a.Auth.Require("admin_user.update"), a.updateAdminUser)
	r.DELETE("/api/v1/admin/admin-users/:id", a.Auth.Require("admin_user.delete"), a.deleteAdminUser)
	r.POST("/api/v1/admin/admin-users/:id/unlock", a.Auth.Require("admin_user.update"), a.unlockAdminUser)
	r.POST("/api/v1/admin/admin-users/:id/reset-password", a.Auth.Require("admin_user.reset_password"), a.resetAdminPassword)
	r.POST("/api/v1/admin/admin-users/:id/mfa", a.Auth.Require("admin_user.update"), a.adminUserMFA)
}

// listAdminUsers 从请求提取分页及当前档案，返回账号列表或错误映射。
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

// updateAdminUser 从请求提取 id 及严格更新输入，将审计信息交给用例事务，返回撤销标识。
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

// deleteAdminUser 将 c 目标与审计信息交给软删事务，返回 deleted 标识或状态拒绝。
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

// unlockAdminUser 将 c 目标与审计信息交给解锁事务，返回 unlocked 标识或错误。
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

// resetAdminPassword 从请求严格读取新口令、目标及审计信息，返回重置成功标识，不生成散列或快照。
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

// adminUserMFA 从请求提取目标及证明，输出调用方事务的登记／确认／关闭形状，不读取账号密钥。
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
