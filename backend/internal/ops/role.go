package ops

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"chargepilot/internal/domain/operations"
	"gorm.io/gorm"
)

// roleCodePattern 限制角色编码为小写字母开头、3–64 位字母数字或下划线。
var roleCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)

// RoleInput 是角色创建和更新参数，更新不改编码，内置角色只改名称和说明。
type RoleInput struct {
	Code        string   `json:"code"`        // 合法角色编码
	Name        string   `json:"name"`        // 最多128字符的非空名称
	Description *string  `json:"description"` // 最多255字符的可空说明
	Permissions []string `json:"permissions"` // 非空、无重复且只能授予当前持有的权限
}

// validateShape 检查角色编码、名称和说明。
func (in RoleInput) validateShape(message string) error {
	if !roleCodePattern.MatchString(in.Code) || in.Name == "" || utf8.RuneCountInString(in.Name) > 128 || (in.Description != nil && utf8.RuneCountInString(*in.Description) > 255) || len(in.Permissions) == 0 {
		return &ManagementInputError{message}
	}
	return nil
}

// validateGrant 检查去除空白后的权限编码均存在且不重复，失败返回输入提示。
func (in RoleInput) validateGrant(codes []string, message string) error {
	known := make(map[string]bool, len(codes))
	for _, code := range codes {
		known[code] = true
	}
	seen := make(map[string]bool, len(in.Permissions))
	for _, code := range in.Permissions {
		code = strings.TrimSpace(code)
		if !known[code] || seen[code] {
			return &ManagementInputError{message}
		}
		seen[code] = true
	}
	return nil
}

// RoleService 负责当前角色权限检查、串行赋权或删除，以及同事务审计。
type RoleService struct {
	DB *gorm.DB /* 所属角色数据库 */
}

// List 核对当前 admin_user.read 权限后查询角色列表。
func (s RoleService) List(ctx context.Context, actor operations.Profile) ([]RoleRow, error) {
	if _, err := readManagementActor(ctx, s.DB, actor, "admin_user.read"); err != nil {
		return nil, err
	}
	return (RoleRepo{DB: s.DB}).ListRoles(ctx)
}

// Permissions 核对读取权限后查询权限字典，过滤退役编码。
func (s RoleService) Permissions(ctx context.Context, actor operations.Profile) ([]PermissionRow, error) {
	if _, err := readManagementActor(ctx, s.DB, actor, "admin_user.read"); err != nil {
		return nil, err
	}
	rows, err := (RoleRepo{DB: s.DB}).ListPermissions(ctx)
	if err != nil {
		return nil, err
	}
	visible := make([]PermissionRow, 0, len(rows))
	for _, row := range rows {
		if !operations.RetiredPermission(row.Code) {
			visible = append(visible, row)
		}
	}
	return visible, nil
}

// Create 核对创建权限后保存角色、完整权限和审计。
// 权限须在集合锁后用当前读核对，不能再次授予已撤销权限。
func (s RoleService) Create(ctx context.Context, audit AuditContext, input RoleInput) (uint64, error) {
	message := "角色无效：编码须为小写字母开头的 3–64 位，名称必填，权限须非空且只能从自己已持有的权限中选择"
	if err := input.validateShape(message); err != nil {
		return 0, err
	}
	if s.DB == nil {
		return 0, errors.New("ops management unavailable")
	}
	var id uint64
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, err := lockedManagementActor(tx, audit.Actor, "role.create")
		if err != nil {
			return err
		}
		audit.Actor = actor
		if err := input.validateGrant(actor.Permissions, message); err != nil {
			return err
		}
		id, err = (RoleRepo{DB: tx}).InsertRole(tx, input.Code, input.Name, input.Description)
		if err != nil {
			return err
		}
		if err := ReplaceRolePermissions(tx, id, input.Permissions); err != nil {
			return err
		}
		return AppendAuditInTx(tx, audit, AuditChange{Action: "create", Target: "role", ID: id, After: input})
	})
	return id, err
}

// Update 核对更新权限后修改角色名称、说明和权限，编码不变，内置角色权限不变。
// 角色和权限须在锁内读取，审计记录实际保存的权限。
func (s RoleService) Update(ctx context.Context, audit AuditContext, id uint64, input RoleInput) (bool, error) {
	message := "角色无效：只能从自己已持有的权限中选择"
	if err := input.validateShape(message); err != nil {
		return false, err
	}
	if s.DB == nil {
		return false, errors.New("ops management unavailable")
	}
	locked := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, err := lockedManagementActor(tx, audit.Actor, "role.update")
		if err != nil {
			return err
		}
		audit.Actor = actor
		if err := input.validateGrant(actor.Permissions, message); err != nil {
			return err
		}
		store := RoleRepo{DB: tx}
		before, err := store.LockRole(tx, id)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrManagementRoleMissing
		}
		if err != nil {
			return err
		}
		before.Permissions, err = store.RolePermissionCodesInTx(tx, id)
		if err != nil {
			return err
		}
		if err := store.UpdateRoleName(tx, id, input.Name, input.Description); err != nil {
			return err
		}
		locked = before.IsBuiltin
		if !locked {
			if err := ReplaceRolePermissions(tx, id, input.Permissions); err != nil {
				return err
			}
		}
		after := input
		after.Code = before.Code
		if locked {
			after.Permissions = before.Permissions
		} else {
			after.Permissions, err = store.RolePermissionCodesInTx(tx, id)
			if err != nil {
				return err
			}
		}
		return AppendAuditInTx(tx, audit, AuditChange{Action: "update", Target: "role", ID: id, Before: before, After: after})
	})
	return locked, err
}

// Delete 核对删除权限、内置角色、本人角色和账号引用后软删角色。
// 角色分配和删除共用集合锁，账号引用用当前读；角色、权限和审计同事务。
func (s RoleService) Delete(ctx context.Context, audit AuditContext, id uint64) error {
	if s.DB == nil {
		return errors.New("ops management unavailable")
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, err := lockedManagementActor(tx, audit.Actor, "role.delete")
		if err != nil {
			return err
		}
		audit.Actor = actor
		store := RoleRepo{DB: tx}
		before, err := store.LockRole(tx, id)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrManagementRoleMissing
		}
		if err != nil {
			return err
		}
		if before.IsBuiltin {
			return ErrBuiltinRoleDelete
		}
		if before.Code == actor.Role {
			return ErrOwnRoleDelete
		}
		count, err := store.CountRoleUsersInTx(tx, id)
		if err != nil {
			return err
		}
		if count > 0 {
			return ErrRoleInUse
		}
		before.Permissions, err = store.RolePermissionCodesInTx(tx, id)
		if err != nil {
			return err
		}
		if err := store.SoftDeleteRoleAndPermissions(tx, id); err != nil {
			return err
		}
		return AppendAuditInTx(tx, audit, AuditChange{Action: "delete", Target: "role", ID: id, Before: before})
	})
}
