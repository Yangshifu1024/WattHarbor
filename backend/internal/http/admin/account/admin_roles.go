package account

import (
	"github.com/gin-gonic/gin"
	adminhttp "wattharbor/internal/http/admin"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/httpapi"
)

// RegisterRoles 注册角色列表、权限字典和角色管理接口。
func (a AdminAccounts) RegisterRoles(r *gin.Engine) {
	r.GET("/api/v1/admin/roles", a.Auth.Require("admin_user.read"), a.listRoles)
	r.GET("/api/v1/admin/permissions", a.Auth.Require("admin_user.read"), a.listPermissions)
	r.POST("/api/v1/admin/roles", a.Auth.Require("role.create"), a.createRole)
	r.PUT("/api/v1/admin/roles/:id", a.Auth.Require("role.update"), a.updateRole)
	r.DELETE("/api/v1/admin/roles/:id", a.Auth.Require("role.delete"), a.deleteRole)
}

// listRoles 返回当前管理员可读取的角色列表。
func (a AdminAccounts) listRoles(c *gin.Context) {
	rows, err := a.Roles.List(c.Request.Context(), adminhttp.AdminAuditContext(c).Actor)
	if err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// listPermissions 返回当前管理员可读取的权限字典。
func (a AdminAccounts) listPermissions(c *gin.Context) {
	rows, err := a.Roles.Permissions(c.Request.Context(), adminhttp.AdminAuditContext(c).Actor)
	if err != nil {
		adminhttp.AdminManagementFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// createRole 创建角色并返回 ID。
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

// updateRole 更新角色，返回 permissions_locked 表示权限集合是否受保护。
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

// deleteRole 删除指定角色并返回 ID。
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
