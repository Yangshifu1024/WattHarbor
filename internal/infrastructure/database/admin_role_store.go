package database

import (
	"context"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 后台角色与权限字典的读写。
//
// 两条硬规则：授予角色的权限不得超出操作者自己持有的权限；内置角色的权限
// 集合锁定，只能改名称与说明。删不掉仍有账号在用的角色——否则那些账号会
// 变成没有角色也没有权限的孤儿。

// RoleRow 是角色列表与详情的一行。Permissions 与 UserCount 由查询后回填。
type RoleRow struct {
	ID          uint64   `json:"id" gorm:"column:id"`                   // 角色主键
	Code        string   `json:"code" gorm:"column:code"`               // 角色编码，小写字母开头 3–64 位
	Name        string   `json:"name" gorm:"column:name"`               // 角色名称，必填，最多 128 字符
	Description *string  `json:"description" gorm:"column:description"` // 角色说明，可空
	IsBuiltin   bool     `json:"is_builtin" gorm:"column:is_builtin"`   // 内置角色的权限集合锁定，不可改也不可删
	ActiveCode  *string  `json:"active_code" gorm:"column:active_code"` // 未删除时的角色编码（由 code 派生的生成列），软删除后为 NULL
	Permissions []string `json:"permissions" gorm:"-"`                  // 该角色持有的权限编码，按字典序；无权限时返回 [] 而不是 null
	UserCount   int64    `json:"user_count" gorm:"-"`                   // 仍挂在这个角色上的未删除账号数
}

// PermissionRow 是权限字典的一行，角色编辑器勾选权限时读的就是这张表。
type PermissionRow struct {
	ID          uint64 `json:"id" gorm:"column:id"`                   // 权限主键
	Code        string `json:"code" gorm:"column:code"`               // 权限编码，路由鉴权用的就是它
	Name        string `json:"name" gorm:"column:name"`               // 权限中文名，前端按模块分组展示
	Module      string `json:"module" gorm:"column:module"`           // 所属模块，如 alert、pricing、webhook
	Description string `json:"description" gorm:"column:description"` // 权限说明，库中为 NULL 时由 SQL 补空串
}

// RoleStore 是角色与权限字典的读写入口。
type RoleStore struct{ DB *gorm.DB }

// PermissionCodesForRoles 取出这些角色合起来持有的权限编码（去重）。
// 被软删除的角色不参与：删掉的角色不该继续给人授权。
func (s RoleStore) PermissionCodesForRoles(ctx context.Context, codes ...string) ([]string, error) {
	var result []string
	err := s.DB.WithContext(ctx).Table("permission AS p").
		Joins("JOIN role_permission rp ON rp.permission_id = p.id").
		Joins("JOIN role r ON r.id = rp.role_id AND r.deleted_at IS NULL").
		Where("r.code IN ?", codes).Distinct("p.code").Pluck("p.code", &result).Error
	if err != nil {
		return nil, err
	}
	return operations.WithoutRetiredPermissions(result), nil
}

// ListRoles 按 ID 升序返回未删除角色，并回填每个角色的权限编码与账号数。
func (s RoleStore) ListRoles(ctx context.Context) ([]RoleRow, error) {
	rows := []RoleRow{}
	if err := s.DB.WithContext(ctx).Table("role AS r").Where("r.deleted_at IS NULL").Order("r.id").Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		codes, err := s.RolePermissionCodes(ctx, rows[i].ID)
		if err != nil {
			return nil, err
		}
		rows[i].Permissions = codes
		if rows[i].UserCount, err = s.CountRoleUsers(ctx, rows[i].ID); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// RolePermissionCodes 返回单个角色持有的权限编码，按字典序。
func (s RoleStore) RolePermissionCodes(ctx context.Context, roleID uint64) ([]string, error) {
	codes := []string{}
	err := s.DB.WithContext(ctx).Table("permission AS p").
		Joins("JOIN role_permission rp ON rp.permission_id = p.id").
		Where("rp.role_id = ?", roleID).Order("p.code").Pluck("p.code", &codes).Error
	if err != nil {
		return nil, err
	}
	return operations.WithoutRetiredPermissions(codes), nil
}

// CountRoleUsers 数出仍挂在这个角色上的未删除账号。大于 0 时不允许删除该角色。
func (s RoleStore) CountRoleUsers(ctx context.Context, roleID uint64) (int64, error) {
	var count int64
	err := s.DB.WithContext(ctx).Table("admin_user").
		Where("role_id = ? AND deleted_at IS NULL", roleID).Count(&count).Error
	return count, err
}

// ListPermissions 按模块、编码排序返回权限列表，分组由前端处理。
// feedback.% 归到 device 分组：那些权限实际管的是设备侧。
func (s RoleStore) ListPermissions(ctx context.Context) ([]PermissionRow, error) {
	rows := []PermissionRow{}
	err := s.DB.WithContext(ctx).Table("permission").
		Select("id, code, name, CASE WHEN code LIKE 'feedback.%' THEN 'device' ELSE module END AS module, " +
			"COALESCE(description, '') AS description").
		Order("module, code").Find(&rows).Error
	return rows, err
}

// InsertRole 落一个角色，返回新角色 ID。自定义角色的 is_builtin 恒为 false。
func (s RoleStore) InsertRole(tx *gorm.DB, code, name string, description *string) (uint64, error) {
	if err := tx.Table("role").Create(map[string]any{
		"code": code, "name": name, "description": description, "is_builtin": false,
	}).Error; err != nil {
		return 0, err
	}
	return LastInsertID(tx)
}

// FindRole 取一个未删除角色，取不到返回 gorm.ErrRecordNotFound。
func (s RoleStore) FindRole(ctx context.Context, db *gorm.DB, id uint64) (RoleRow, error) {
	var row RoleRow
	err := db.WithContext(ctx).Table("role").Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// UpdateRoleName 只改名称与说明。内置角色也只能改这两项。
func (s RoleStore) UpdateRoleName(tx *gorm.DB, id uint64, name string, description *string) error {
	return tx.Table("role").Where("id = ?", id).
		Updates(map[string]any{"name": name, "description": description}).Error
}

// ReplaceRolePermissions 用给定集合整体替换角色的权限。
// 整体替换而非增删：权限集合的最终状态由入参决定，重放同一次提交不会
// 留下半截的中间状态。role_permission 存的是 permission_id，所以按编码
// 子查询换取；空白编码直接跳过。
func ReplaceRolePermissions(tx *gorm.DB, roleID uint64, codes []string) error {
	if err := tx.Exec("DELETE FROM role_permission WHERE role_id = ?", roleID).Error; err != nil {
		return err
	}
	for _, code := range codes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if err := tx.Exec("INSERT INTO role_permission (role_id, permission_id) SELECT ?, id FROM permission WHERE code = ?", roleID, code).Error; err != nil {
			return err
		}
	}
	return nil
}

// LockRole 在行锁下取一个未删除角色，返回更新前快照供审计留痕。
func (s RoleStore) LockRole(tx *gorm.DB, id uint64) (RoleRow, error) {
	var row RoleRow
	err := tx.Table("role").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// SoftDeleteRole 软删除角色。仍挂有账号时返回 0——调用方据此拒绝删除。
// 软删除而非物理删除：审计记录要靠它追溯谁在什么时候还能拿到这个角色。
func (s RoleStore) SoftDeleteRole(tx *gorm.DB, id uint64) (int64, error) {
	res := tx.Table("role").Where("id = ? AND deleted_at IS NULL", id).
		Updates(map[string]any{"deleted_at": gorm.Expr("UTC_TIMESTAMP(3)")})
	return res.RowsAffected, res.Error
}
