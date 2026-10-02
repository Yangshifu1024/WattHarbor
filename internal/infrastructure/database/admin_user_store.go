package database

import (
	"context"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 后台账号与角色的管理读写。登录、改密与 MFA 校验在 admin_account.go，
// 这里承接列表、创建、编辑、停用、软删除与角色可授予性判断。
//
// auth_version 每次敏感变更都 +1：它进 JWT，改密、停用、删除后旧令牌立刻失效。

// AdminUserRow 是 PC 后台管理员账号（admin_user 表）的列表行映射。
// 账号归属 central_db，与终端用户 central_db.user 分开存放；一个账号一个主角色。
// 可空字段一律用指针：角色被软删除与"角色 ID 为 0"在响应里是两回事。
type AdminUserRow struct {
	ID            uint64  `json:"id" gorm:"column:id"`                     // 账号主键
	Username      string  `json:"username" gorm:"column:username"`         // 登录用户名，全局唯一
	DisplayName   *string `json:"display_name" gorm:"column:display_name"` // 显示名称；nil 表示未设置
	RoleID        *uint64 `json:"role_id" gorm:"column:role_id"`           // 主角色 ID；nil 表示尚未分配角色
	RoleCode      *string `json:"role_code" gorm:"column:role_code"`       // 主角色编码，由 role 表联查；角色已删时为 nil
	RoleName      *string `json:"role_name" gorm:"column:role_name"`       // 角色名称；角色已删时为空
	Phone         *string `json:"phone" gorm:"column:phone"`               // 备用手机号；nil 表示未登记
	Email         *string `json:"email" gorm:"column:email"`               // 邮箱；nil 表示未登记
	Status        string  `json:"status" gorm:"column:status"`             // active 正常 / disabled 停用 / locked 连续登录失败临时锁定
	MFAEnabled    bool    `json:"mfa_enabled" gorm:"column:mfa_enabled"`   // 是否已启用双因素认证
	LastLoginAt   *string `json:"last_login_at" gorm:"column:last_login_at"`
	LockedUntil   *string `json:"locked_until" gorm:"column:locked_until"`
	FailedLogins  uint32  `json:"failed_login_count" gorm:"column:failed_login_count"` // 连续登录失败次数
	DeletePending bool    `json:"-" gorm:"-"`                                          // 是否有待处理的下线流程，本列表不使用、不输出
}

const adminUserSelect = "u.id,u.username,u.display_name,u.role_id," +
	"r.code AS role_code,r.name AS role_name,u.phone,u.email,u.status," +
	"u.mfa_enabled,u.last_login_at,u.locked_until,u.failed_login_count"

// ListAdminUsers 分页返回未删除的后台账号，可按状态与用户名/姓名关键字过滤。
// 关键字列不转义通配符，保留既有搜索行为（与其它后台列表不同）。
func (s AdminStore) ListAdminUsers(ctx context.Context, q PageQuery) (Page[AdminUserRow], error) {
	out := Page[AdminUserRow]{Items: []AdminUserRow{}, Page: q.Page, PageSize: q.PageSize}
	query := s.DB.WithContext(ctx).Table("admin_user AS u").Where("u.deleted_at IS NULL")
	if q.Status != "" {
		query = query.Where("u.status = ?", q.Status)
	}
	if q.Keyword != "" {
		query = query.Where("u.username LIKE ? OR u.display_name LIKE ?", dbutil.LikePattern(q.Keyword), dbutil.LikePattern(q.Keyword))
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Select(adminUserSelect).
		Joins("LEFT JOIN role AS r ON r.id = u.role_id AND r.deleted_at IS NULL").
		Order("u.id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error
	return out, err
}

// AssignableRoleCodes 返回角色持有的权限编码，角色不存在、已软删除或权限为空时返回空切片。
// 调用方据此判断能否授予该角色，并逐条比对是否超出自己的权限。
func (s AdminStore) AssignableRoleCodes(ctx context.Context, db *gorm.DB, roleID uint64) ([]string, error) {
	codes := []string{}
	err := db.WithContext(ctx).Table("role AS r").
		Joins("JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id").
		Where("r.id=? AND r.deleted_at IS NULL", roleID).Pluck("p.code", &codes).Error
	if err != nil {
		return nil, err
	}
	return operations.WithoutRetiredPermissions(codes), nil
}

// CountOtherActiveAdmins 数出除指定账号外仍有效的客户管理员。
// 删除前必须至少剩一个，否则最后一个管理员会把自己锁在门外。
func (s AdminStore) CountOtherActiveAdmins(ctx context.Context, db *gorm.DB, id uint64) (int64, error) {
	var remaining int64
	err := db.WithContext(ctx).Table("admin_user AS u").
		Joins("JOIN role AS r ON r.id = u.role_id").
		Where("u.deleted_at IS NULL AND u.status = 'active' AND r.code = 'customer_admin' AND r.deleted_at IS NULL").
		Where("u.id <> ?", id).Count(&remaining).Error
	return remaining, err
}

// LockAdminUser 在行锁下取一个未删除账号，返回更新前快照供审计留痕。
func (s AdminStore) LockAdminUser(tx *gorm.DB, id uint64) (map[string]any, error) {
	var before map[string]any
	err := tx.Table("admin_user").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error
	return before, err
}

// InsertAdminUser 落一个后台账号，返回新账号 ID。密码散列由调用方算好传入。
func (s AdminStore) InsertAdminUser(tx *gorm.DB, values map[string]any) (uint64, error) {
	if err := tx.Table("admin_user").Create(values).Error; err != nil {
		return 0, err
	}
	return LastInsertID(tx)
}

// adminUserColumns 是账号更新时允许写入的列。auth_version 由调用方按
// "bumpAuthVersion" 决定是否 +1，不是每次更新都要动。
var adminUserColumns = map[string]bool{
	"display_name": true, "role_id": true, "phone": true, "email": true,
	"status": true, "mfa_secret": true, "mfa_enabled": true,
	"password_hash": true, "failed_login_count": true, "locked_until": true,
}

// UpdateAdminUser 写入账号字段，并按需推进 auth_version 让旧令牌失效。
// 调用方需自行组装 values；这里只负责补 auth_version 与执行更新。
func UpdateAdminUser(tx *gorm.DB, id uint64, values map[string]any, bumpAuthVersion bool) error {
	if bumpAuthVersion {
		values["auth_version"] = gorm.Expr("auth_version + 1")
	}
	return tx.Table("admin_user").Where("id = ?", id).Updates(values).Error
}

// SoftDeleteAdminUser 软删除账号：记删除人与时间、停用并使旧令牌失效。
// 不物理删除——审计记录要靠它追溯。
func SoftDeleteAdminUser(tx *gorm.DB, id, operatorID uint64, at time.Time) error {
	return tx.Table("admin_user").Where("id = ?", id).Updates(map[string]any{
		"deleted_at": at, "deleted_by": operatorID,
		"status": "disabled", "auth_version": gorm.Expr("auth_version + 1"),
	}).Error
}

// FindAdminUserByUsername 按登录名取一个未删除账号。
func (s AdminStore) FindAdminUserByUsername(ctx context.Context, db *gorm.DB, username string) (AdminUserRow, error) {
	var row AdminUserRow
	err := db.WithContext(ctx).Table("admin_user AS u").
		Select("u.id,u.username,u.display_name,u.role_id,u.phone,u.email,u.status,u.mfa_enabled").
		Where("u.username = ? AND u.deleted_at IS NULL", username).Take(&row).Error
	return row, err
}

// CountAdminUsersByFilter 按给定条件统计账号数，供账号唯一性与配额校验使用。
func (s AdminStore) CountAdminUsersByFilter(ctx context.Context, db *gorm.DB, where string, args ...any) (int64, error) {
	var count int64
	err := db.WithContext(ctx).Table("admin_user").Where("deleted_at IS NULL").
		Where(where, args...).Count(&count).Error
	return count, err
}

// FindAdminUsername 取账号登录名。验证器标签必须绑定到被管理的账号，
// 因此这里按主键精确取，不用登录名。
func (s AdminStore) FindAdminUsername(ctx context.Context, db *gorm.DB, id uint64) (string, error) {
	var row struct {
		Username string `gorm:"column:username"`
	}
	err := db.WithContext(ctx).Table("admin_user").Select("username").
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	return row.Username, err
}

// UpdateAdminMFASecret 写入 MFA 密钥。enroll 阶段密钥未启用，
// confirm 验证通过后才启用；两者都要让旧会话失效。
func UpdateAdminMFASecret(tx *gorm.DB, id uint64, secret *string, enabled bool, bumpAuthVersion bool) error {
	values := map[string]any{"mfa_secret": secret, "mfa_enabled": enabled}
	if bumpAuthVersion {
		values["auth_version"] = gorm.Expr("auth_version + 1")
	}
	return tx.Table("admin_user").Where("id = ? AND deleted_at IS NULL", id).Updates(values).Error
}

// UpdateAdminMFAStaged 写入尚未启用的 MFA 密钥。不动 auth_version：
// 登记阶段没有改变任何认证事实，强制旧会话失效只会打断正在配置的人。
func UpdateAdminMFAStaged(tx *gorm.DB, id uint64, secret string) error {
	return tx.Table("admin_user").Where("id = ? AND deleted_at IS NULL", id).
		Updates(map[string]any{"mfa_secret": secret, "mfa_enabled": false}).Error
}

// LockActiveAdmin 在行锁下取一个启用且未删除的后台账号。
// MFA 开通与关闭都要先在这里拿账号，再比对 auth_version——
// 版本对不上说明会话已被别处作废（改密、停用、删除），必须让操作重新登录。
func (s AdminStore) LockActiveAdmin(tx *gorm.DB, id uint64) (AdminAccount, error) {
	var row AdminAccount
	err := tx.Table("admin_user").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND status='active' AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}
