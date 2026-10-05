package account

import (
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	adminhttp "github.com/ChargePilot2026/charge-pilot/internal/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// Register 在r挂载原账号读取和管理路径，创建路径仍由运营族注册，避免重复注册。
func (a AdminAccounts) Register(r *gin.Engine) {
	r.GET("/api/v1/admin/admin-users", a.Auth.Require("admin_user.read"), a.listAdminUsers)
	r.PUT("/api/v1/admin/admin-users/:id", a.Auth.Require("admin_user.update"), a.updateAdminUser)
	r.DELETE("/api/v1/admin/admin-users/:id", a.Auth.Require("admin_user.delete"), a.deleteAdminUser)
	r.POST("/api/v1/admin/admin-users/:id/unlock", a.Auth.Require("admin_user.update"), a.unlockAdminUser)
	r.POST("/api/v1/admin/admin-users/:id/reset-password", a.Auth.Require("admin_user.reset_password"), a.resetAdminPassword)
	r.POST("/api/v1/admin/admin-users/:id/mfa", a.Auth.Require("admin_user.update"), a.adminUserMFA)
}

// listAdminUsers 从c提取原分页及当前档案，返回所属账号列表或原错误映射。
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

// updateAdminUser 从c提取id及严格更新输入，将原审计事实交给所属完整事务，返回原撤销标识。
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

// deleteAdminUser 将c原目标与审计事实交给所属软删事务，返回原deleted标识或状态拒绝。
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

// unlockAdminUser 将c原目标与审计事实交给所属解锁事务，返回原unlocked标识或错误。
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

// resetAdminPassword 从c严格提取原新口令、目标及审计事实，返回所属重置成功标识，不生成散列或快照。
func (a AdminAccounts) resetAdminPassword(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	var input struct {
		NewPassword string `json:"new_password"` /* 12–72字节新口令，仅交给所属重置 */
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

// adminUserMFA 从c提取原目标及证明，输出所属事务的原登记／确认／关闭形状，不读取账号密钥。
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
