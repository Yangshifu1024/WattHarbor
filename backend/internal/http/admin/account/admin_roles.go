package account

import (
	adminhttp "github.com/ChargePilot2026/charge-pilot/internal/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// RegisterRoles 在r挂载原角色及权限字典路径，HTTP只按原权限注册处理器。
func (a AdminAccounts) RegisterRoles(r *gin.Engine) {
	r.GET("/api/v1/admin/roles", a.Auth.Require("admin_user.read"), a.listRoles)
	r.GET("/api/v1/admin/permissions", a.Auth.Require("admin_user.read"), a.listPermissions)
	r.POST("/api/v1/admin/roles", a.Auth.Require("role.create"), a.createRole)
	r.PUT("/api/v1/admin/roles/:id", a.Auth.Require("role.update"), a.updateRole)
	r.DELETE("/api/v1/admin/roles/:id", a.Auth.Require("role.delete"), a.deleteRole)
}

// listRoles 将c原当前档案交给所属列表，返回原items形状或错误。
func (a AdminAccounts) listRoles(c *gin.Context) {
	rows, err := a.Roles.List(c.Request.Context(), adminhttp.AdminAuditContext(c).Actor)
	if err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// listPermissions 将c原当前档案交给所属字典，返回原排序／分组items，不在HTTP过滤业务权限。
func (a AdminAccounts) listPermissions(c *gin.Context) {
	rows, err := a.Roles.Permissions(c.Request.Context(), adminhttp.AdminAuditContext(c).Actor)
	if err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// createRole 从c严格提取原角色输入及审计事实，返回所属完整创建事务的ID或错误。
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

// updateRole 从c提取原id、输入和审计事实，返回所属更新的内置权限锁定标识或错误。
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

// deleteRole 将c原id和审计事实交给所属角色删除事务，返回原ID或状态拒绝。
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
