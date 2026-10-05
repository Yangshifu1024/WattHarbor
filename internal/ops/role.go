package ops

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"gorm.io/gorm"
)

// roleCodePattern 保留小写字母开头、3–64位字母数字下划线的原角色编码。
var roleCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)

// RoleInput 保留原创建／更新字段；更新保留原编码，内置角色只修改名称与说明。
type RoleInput struct {
	Code        string   `json:"code"`        // 原合法角色编码
	Name        string   `json:"name"`        // 最多128字符的非空名称
	Description *string  `json:"description"` // 最多255字符的可空说明
	Permissions []string `json:"permissions"` // 非空、无重复且只能授予当前持有的权限
}

// validateShape 核对in编码、名称及说明的原约束，返回输入错误，无IO。
func (in RoleInput) validateShape(message string) error {
	if !roleCodePattern.MatchString(in.Code) || in.Name == "" || utf8.RuneCountInString(in.Name) > 128 || (in.Description != nil && utf8.RuneCountInString(*in.Description) > 255) || len(in.Permissions) == 0 {
		return &ManagementInputError{message}
	}
	return nil
}

// validateGrant 核对in权限经trim后都属于codes且不重复，返回原输入提示message或nil，无IO。
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

// RoleService 拥有实时角色权限、集合串行化、赋权／删除约束及安全审计事务。
type RoleService struct {
	DB *gorm.DB /* 所属角色数据库 */
}

// List 在ctx下重读actor的admin_user.read权限，返回原排序角色列表或授权／存储错误，只读。
func (s RoleService) List(ctx context.Context, actor operations.Profile) ([]RoleRow, error) {
	if _, err := readManagementActor(ctx, s.DB, actor, "admin_user.read"); err != nil {
		return nil, err
	}
	return (RoleRepo{DB: s.DB}).ListRoles(ctx)
}

// Permissions 在ctx下重读actor读取权限，返回原分组字典并过滤退役项或读取错误，只读。
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

// Create 在ctx下以audit当前create权限建立input角色／完整权限集合并同事务审计，返回ID或错误。
// 集合锁后才当前读操作者权限，不能把事务外或旧RR中的已撤销权限再次授予。
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

// Update 在ctx下以audit当前update权限修改id的input，返回是否锁定内置权限或输入／缺失／存储错误。
// 当前角色行与集合始终在事务内读取；编码保留，内置权限固定，审计记录实际最终权限而非被忽略输入。
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

// Delete 在ctx下以audit当前delete权限软删除id，当前核对内置／本人角色与全部未删除账号引用。
// 分配和删除共享集合锁，当前计数不得因旧RR漏掉已提交分配；角色、权限和审计整体提交或回滚。
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
