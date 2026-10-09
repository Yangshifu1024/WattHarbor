package account

import (
	adminhttp "chargepilot/internal/http/admin"
	"chargepilot/internal/ops"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// RegisterRoles 在 r 挂载角色及权限字典路径，HTTP 只按权限注册处理器。
func (a AdminAccounts) RegisterRoles(r *gin.Engine) {
	r.GET("/api/v1/admin/roles", a.Auth.Require("admin_user.read"), a.listRoles)
	r.GET("/api/v1/admin/permissions", a.Auth.Require("admin_user.read"), a.listPermissions)
	r.POST("/api/v1/admin/roles", a.Auth.Require("role.create"), a.createRole)
	r.PUT("/api/v1/admin/roles/:id", a.Auth.Require("role.update"), a.updateRole)
	r.DELETE("/api/v1/admin/roles/:id", a.Auth.Require("role.delete"), a.deleteRole)
}

// listRoles 将 c 当前档案交给列表，返回 items 形状或错误。
func (a AdminAccounts) listRoles(c *gin.Context) {
	rows, err := a.Roles.List(c.Request.Context(), adminhttp.AdminAuditContext(c).Actor)
	if err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// listPermissions 将 c 当前档案交给字典，返回排序／分组 items，不在 HTTP 过滤业务权限。
func (a AdminAccounts) listPermissions(c *gin.Context) {
	rows, err := a.Roles.Permissions(c.Request.Context(), adminhttp.AdminAuditContext(c).Actor)
	if err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// createRole 从请求严格读取角色输入及审计信息，返回完整创建事务的 ID 或错误。
func (a AdminAccounts) createRole(c *gin.Context) {
	var input ops.RoleInput
	if !httpapi.DecodeResource(c, &input) {
		return
	}
	id, err := a.Roles.Create(c.Request.Context(), adminhttp.AdminAuditContext(c), input)
	if err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// updateRole 从请求提取 id、输入和审计信息，返回更新的内置权限锁定标识或错误。
func (a AdminAccounts) updateRole(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	var input ops.RoleInput
	if !httpapi.DecodeResource(c, &input) {
		return
	}
	locked, err := a.Roles.Update(c.Request.Context(), adminhttp.AdminAuditContext(c), id, input)
	if err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "permissions_locked": locked})
}

// deleteRole 将 cid 和审计信息交给角色删除事务，返回 ID 或状态拒绝。
func (a AdminAccounts) deleteRole(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	if err := a.Roles.Delete(c.Request.Context(), adminhttp.AdminAuditContext(c), id); err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}
