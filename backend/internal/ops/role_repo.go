package ops

import (
	"context"
	"strings"
	"wattharbor/internal/persist"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"wattharbor/internal/domain/operations"
)

// 角色权限不能超过操作者自己的权限；内置角色只可改名称和说明。
// 仍有账号引用的角色不能删除。

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

// RoleRepo 是角色与权限字典的读写入口。
type RoleRepo struct {
	DB *gorm.DB /* 所属角色与权限数据库 */
}

// PermissionCodesForRoles 合并有效角色权限并去重，排除已软删角色。
func (s RoleRepo) PermissionCodesForRoles(ctx context.Context, codes ...string) ([]string, error) {
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

// ListRoles 按 ID 升序读取未删除角色，并补充权限和账号数。
func (s RoleRepo) ListRoles(ctx context.Context) ([]RoleRow, error) {
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

// RolePermissionCodes 按字典序读取未退役权限，用于普通查询。
func (s RoleRepo) RolePermissionCodes(ctx context.Context, roleID uint64) ([]string, error) {
	codes := []string{}
	err := s.DB.WithContext(ctx).Table("permission AS p").
		Joins("JOIN role_permission rp ON rp.permission_id = p.id").
		Where("rp.role_id = ?", roleID).Order("p.code").Pluck("p.code", &codes).Error
	if err != nil {
		return nil, err
	}
	return operations.WithoutRetiredPermissions(codes), nil
}

// RolePermissionCodesInTx 在集合和角色锁内用共享锁读取当前权限，排除退役编码和旧快照中的已撤销权限。
func (s RoleRepo) RolePermissionCodesInTx(tx *gorm.DB, roleID uint64) ([]string, error) {
	codes := []string{}
	err := tx.Table("permission AS p").Joins("JOIN role_permission rp ON rp.permission_id=p.id").Where("rp.role_id=?", roleID).Clauses(clause.Locking{Strength: "SHARE"}).Order("p.code").Pluck("p.code", &codes).Error
	if err != nil {
		return nil, err
	}
	return operations.WithoutRetiredPermissions(codes), nil
}

// CountRoleUsersInTx 在集合锁内用当前读统计未删除账号引用；分配和删除必须共用该锁。
func (s RoleRepo) CountRoleUsersInTx(tx *gorm.DB, roleID uint64) (int64, error) {
	var count int64
	err := tx.Table("admin_user").Where("role_id=? AND deleted_at IS NULL", roleID).Clauses(clause.Locking{Strength: "UPDATE"}).Count(&count).Error
	return count, err
}

// CountRoleUsers 统计未删除账号引用，只供列表展示；删除检查须用 CountRoleUsersInTx。
func (s RoleRepo) CountRoleUsers(ctx context.Context, roleID uint64) (int64, error) {
	var count int64
	err := s.DB.WithContext(ctx).Table("admin_user").
		Where("role_id = ? AND deleted_at IS NULL", roleID).Count(&count).Error
	return count, err
}

// ListPermissions 按模块和编码排序，feedback.% 归入 device，前端负责分组展示。
func (s RoleRepo) ListPermissions(ctx context.Context) ([]PermissionRow, error) {
	rows := []PermissionRow{}
	err := s.DB.WithContext(ctx).Table("permission").
		Select("id, code, name, CASE WHEN code LIKE 'feedback.%' THEN 'device' ELSE module END AS module, " +
			"COALESCE(description, '') AS description").
		Order("module, code").Find(&rows).Error
	return rows, err
}

// InsertRole 在已锁角色集合的事务中保存自定义角色；调用方核对当前可授予权限并同时保存权限和审计。
func (s RoleRepo) InsertRole(tx *gorm.DB, code, name string, description *string) (uint64, error) {
	if err := tx.Table("role").Create(map[string]any{
		"code": code, "name": name, "description": description, "is_builtin": false,
	}).Error; err != nil {
		return 0, err
	}
	return persist.LastInsertID(tx)
}

// FindRole 读取未删除角色，不加写锁。
func (s RoleRepo) FindRole(ctx context.Context, db *gorm.DB, id uint64) (RoleRow, error) {
	var row RoleRow
	err := db.WithContext(ctx).Table("role").Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// UpdateRoleName 在集合和角色锁内更新名称、说明，编码和内置权限不变；审计同事务。
func (s RoleRepo) UpdateRoleName(tx *gorm.DB, id uint64, name string, description *string) error {
	return tx.Table("role").Where("id = ?", id).
		Updates(map[string]any{"name": name, "description": description}).Error
}

// ReplaceRolePermissions 在集合和角色锁内完整替换权限，并与审计同事务保存。
// 调用方先核对可授予权限，编码转换为 permission_id，空白编码跳过。
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

// LockRole 在已锁角色集合的事务中锁读未删除角色；权限另由 RolePermissionCodesInTx 当前读。
func (s RoleRepo) LockRole(tx *gorm.DB, id uint64) (RoleRow, error) {
	var row RoleRow
	err := tx.Table("role").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// SoftDeleteRole 软删角色并返回影响行数，保留历史审计引用；调用方负责核对账号引用。
func (s RoleRepo) SoftDeleteRole(tx *gorm.DB, id uint64) (int64, error) {
	res := tx.Table("role").Where("id = ? AND deleted_at IS NULL", id).
		Updates(map[string]any{"deleted_at": gorm.Expr("UTC_TIMESTAMP(3)")})
	return res.RowsAffected, res.Error
}

// SoftDeleteRoleAndPermissions 同时软删角色和权限关联。
// 调用方先锁集合和角色，核对非内置、非本人且无账号引用，并同事务保存审计。
func (s RoleRepo) SoftDeleteRoleAndPermissions(tx *gorm.DB, id uint64) error {
	if _, err := s.SoftDeleteRole(tx, id); err != nil {
		return err
	}
	return DeleteRolePermissions(tx, id)
}

// DeleteRolePermissions 在集合和角色锁内清空权限关联，与角色变更和审计同事务。
func DeleteRolePermissions(tx *gorm.DB, roleID uint64) error {
	return tx.Exec("DELETE FROM role_permission WHERE role_id = ?", roleID).Error
}
