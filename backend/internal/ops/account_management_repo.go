package ops

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/persist"
	"context"
	"time"

	"chargepilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 后台账号管理负责列表、创建、编辑、停用、软删和角色分配；登录、改密和 MFA 校验在 account_repo.go。
// 敏感变更提升 auth_version，使旧会话失效。

// AdminUserRow 是后台账号列表记录，每个账号有一个主角色，与终端用户分表保存。
// 可空字段保留 NULL，不能用角色 ID=0 代替已删除角色。
type AdminUserRow struct {
	ID            uint64          `json:"id" gorm:"column:id"`                                 // 账号主键
	Username      string          `json:"username" gorm:"column:username"`                     // 登录用户名，全局唯一
	DisplayName   *string         `json:"display_name" gorm:"column:display_name"`             // 显示名称；nil 表示未设置
	RoleID        *uint64         `json:"role_id" gorm:"column:role_id"`                       // 主角色 ID；nil 表示尚未分配角色
	RoleCode      *string         `json:"role_code" gorm:"column:role_code"`                   // 主角色编码，由 role 表联查；角色已删时为 nil
	RoleName      *string         `json:"role_name" gorm:"column:role_name"`                   // 角色名称；角色已删时为空
	Phone         *string         `json:"phone" gorm:"column:phone"`                           // 备用手机号；nil 表示未登记
	Email         *string         `json:"email" gorm:"column:email"`                           // 邮箱；nil 表示未登记
	Status        AdminUserStatus `json:"status" gorm:"column:status"`                         // active 正常 / disabled 停用 / locked 连续登录失败临时锁定
	MFAEnabled    bool            `json:"mfa_enabled" gorm:"column:mfa_enabled"`               // 是否已启用双因素认证
	LastLoginAt   *string         `json:"last_login_at" gorm:"column:last_login_at"`           // 最近成功登录时刻，未登录为空
	LockedUntil   *string         `json:"locked_until" gorm:"column:locked_until"`             // 临时锁定截止，未锁定为空
	FailedLogins  uint32          `json:"failed_login_count" gorm:"column:failed_login_count"` // 连续登录失败次数
	DeletePending bool            `json:"-" gorm:"-"`                                          // 是否有待处理的下线流程，本列表不使用、不输出
}

const adminUserSelect = "u.id,u.username,u.display_name,u.role_id," +
	"r.code AS role_code,r.name AS role_name,u.phone,u.email,u.status," +
	"u.mfa_enabled,u.last_login_at,u.locked_until,u.failed_login_count"

// ListAdminUsers 按状态、用户名或姓名关键词分页读取账号；LikePattern 转义通配符。
// q.Status 是跨业务共用的 PageQuery 筛选项，保持 string，在本仓储入口转为 AdminUserStatus。
func (s AccountRepo) ListAdminUsers(ctx context.Context, q persist.PageQuery) (persist.Page[AdminUserRow], error) {
	out := persist.Page[AdminUserRow]{Items: []AdminUserRow{}, Page: q.Page, PageSize: q.PageSize}
	query := s.DB.WithContext(ctx).Table("admin_user AS u").Where("u.deleted_at IS NULL")
	if q.Status != "" {
		query = query.Where("u.status = ?", AdminUserStatus(q.Status))
	}
	if q.Keyword != "" {
		query = query.Where("u.username LIKE ? ESCAPE '!' OR u.display_name LIKE ? ESCAPE '!'", dbutil.LikePattern(q.Keyword), dbutil.LikePattern(q.Keyword))
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Select(adminUserSelect).
		Joins("LEFT JOIN role AS r ON r.id = u.role_id AND r.deleted_at IS NULL").
		Order("u.id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error
	return out, err
}

// LockCustomerAdminRole 锁定有效 customer_admin 角色，串行处理账号分配、角色变更和最后管理员保护。
// 须先于目标账号加锁；后续权限和剩余数量仍用当前读，行锁不会刷新旧 RR 快照。
func (s AccountRepo) LockCustomerAdminRole(tx *gorm.DB) error {
	var row struct {
		ID uint64 /* 用于串行处理管理变更的内置角色主键 */
	}
	return tx.Table("role").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("active_code = ?", "customer_admin").Take(&row).Error
}

// CountOtherActiveAdminsInTx 在 customer_admin 角色锁内用当前读统计其他有效管理员，避免遗漏并发停用或删除。
func (s AccountRepo) CountOtherActiveAdminsInTx(ctx context.Context, tx *gorm.DB, id uint64) (int64, error) {
	var remaining int64
	err := tx.WithContext(ctx).Table("admin_user AS u").
		Joins("JOIN role AS r ON r.id = u.role_id").
		Where("u.deleted_at IS NULL AND u.status = '"+string(AdminUserActive)+"' AND r.code = 'customer_admin' AND r.deleted_at IS NULL").
		Where("u.id <> ?", id).Clauses(clause.Locking{Strength: "UPDATE"}).Count(&remaining).Error
	return remaining, err
}

// AdminUserSnapshot 保存账号管理审计所需资料，排除密码散列和 MFA 密钥。
type AdminUserSnapshot struct {
	ID               uint64          `json:"id"`                 // 账号主键
	Username         string          `json:"username"`           // 登录名
	DisplayName      *string         `json:"display_name"`       // 可空显示名
	RoleID           *uint64         `json:"role_id"`            // 可空角色 ID，按整数保存
	Phone            *string         `json:"phone"`              // 联系号码
	Email            *string         `json:"email"`              // 可空邮箱
	Status           AdminUserStatus `json:"status"`             // 有效／停用／锁定状态
	MFAEnabled       bool            `json:"mfa_enabled"`        // 是否启用 MFA，不含密钥
	LastLoginAt      *time.Time      `json:"last_login_at"`      // 可空登录时刻
	LockedUntil      *time.Time      `json:"locked_until"`       // 可空锁定截止
	FailedLoginCount uint32          `json:"failed_login_count"` // 失败计数
	AuthVersion      uint64          `json:"auth_version"`       // 认证版本，敏感变更后递增，使旧会话失效
	DeletedAt        *time.Time      `json:"deleted_at"`         // 可空删除时刻
	DeletedBy        *uint64         `json:"deleted_by"`         // 可空删除操作人
}

// LockAdminUser 锁读未删除账号的管理资料，排除密码散列和 MFA 密钥。
func (s AccountRepo) LockAdminUser(tx *gorm.DB, id uint64) (AdminUserSnapshot, error) {
	var before AdminUserSnapshot
	err := tx.Table("admin_user").Select("id,username,display_name,role_id,phone,email,status,mfa_enabled,last_login_at,locked_until,failed_login_count,auth_version,deleted_at,deleted_by").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error
	return before, err
}

// InsertAdminUser 创建后台账号并返回主键，密码散列由调用方生成。
func (s AccountRepo) InsertAdminUser(tx *gorm.DB, values map[string]any) (uint64, error) {
	if err := tx.Table("admin_user").Create(values).Error; err != nil {
		return 0, err
	}
	return persist.LastInsertID(tx)
}

// CreateManagementUser 保存账号资料和已生成的密码散列，返回新主键。
func (s AccountRepo) CreateManagementUser(tx *gorm.DB, input AccountCreateInput, hash string) (uint64, error) {
	return s.InsertAdminUser(tx, map[string]any{"username": input.Username, "display_name": input.DisplayName, "password_hash": hash, "phone": input.Phone, "role_id": input.RoleID, "status": AdminUserActive})
}

// UpdateManagementUser 更新固定账号字段；仅角色变化时写角色，仅需撤销会话时提升认证版本。
// input.Status 保持 *string 指针语义：非 nil 才写入，未提交时不碰 admin_user.status。
// 值转换成 AdminUserStatus 只发生在写入该列的那一刻。
func (s AccountRepo) UpdateManagementUser(tx *gorm.DB, id uint64, input AccountUpdateInput, roleChanged, revoke bool) error {
	values := map[string]any{}
	for key, value := range map[string]*string{"display_name": input.DisplayName, "phone": input.Phone, "email": input.Email} {
		if value != nil {
			values[key] = *value
		}
	}
	if input.Status != nil {
		values["status"] = AdminUserStatus(*input.Status)
	}
	if roleChanged {
		values["role_id"] = *input.RoleID
	}
	return UpdateAdminUser(tx, id, values, revoke)
}

// UnlockManagementUser 恢复账号并清空失败次数和锁定，按需提升认证版本。
func (s AccountRepo) UnlockManagementUser(tx *gorm.DB, id uint64, revoke bool) error {
	return UpdateAdminUser(tx, id, map[string]any{"status": AdminUserActive, "locked_until": nil, "failed_login_count": 0}, revoke)
}

// ResetManagementPassword 保存密码散列、提升认证版本并清空失败次数；只解除临时锁定，不启用停用账号。
func (s AccountRepo) ResetManagementPassword(tx *gorm.DB, id uint64, hash string, unlock bool) error {
	values := map[string]any{"password_hash": hash, "failed_login_count": 0, "locked_until": nil}
	if unlock {
		values["status"] = AdminUserActive
	}
	return UpdateAdminUser(tx, id, values, true)
}

// LockManagementAccount 锁读账号密码散列和 MFA 密钥等认证字段，不得直接写入审计或 HTTP 响应。
func (s AccountRepo) LockManagementAccount(tx *gorm.DB, id uint64) (AdminAccount, error) {
	var account AdminAccount
	err := tx.Table("admin_user").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&account).Error
	return account, err
}

// UpdateAdminUser 仅更新本仓储组装的固定字段，按需提升认证版本。
// 调用方持有账号锁并在同一事务保存审计。
func UpdateAdminUser(tx *gorm.DB, id uint64, values map[string]any, bumpAuthVersion bool) error {
	if bumpAuthVersion {
		values["auth_version"] = gorm.Expr("auth_version + 1")
	}
	return tx.Table("admin_user").Where("id = ?", id).Updates(values).Error
}

// SoftDeleteAdminUser 保存删除人和时间，停用账号并提升认证版本，保留历史记录供审计。
// 调用方先检查权限和最后管理员，再同事务保存审计。
func SoftDeleteAdminUser(tx *gorm.DB, id, operatorID uint64, at time.Time) error {
	return tx.Table("admin_user").Where("id = ?", id).Updates(map[string]any{
		"deleted_at": at, "deleted_by": operatorID,
		"status": AdminUserDisabled, "auth_version": gorm.Expr("auth_version + 1"),
	}).Error
}

// UpdateAdminMFASecret 更新 MFA 密钥和启用状态，nil 密钥表示清空，按需撤销旧会话。
// 调用方锁读并验证证明，同事务保存审计；首次未启用登记不提升版本。
func UpdateAdminMFASecret(tx *gorm.DB, id uint64, secret *string, enabled bool, bumpAuthVersion bool) error {
	// 以锁内当前密钥定义代际，启用同一密钥或改密/强制下线不能重置消费记录。
	var current AdminAccount
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&current).Error; err != nil {
		return err
	}
	values := map[string]any{"mfa_secret": secret, "mfa_enabled": enabled}
	changed := (current.MFASecret == nil) != (secret == nil) || (current.MFASecret != nil && secret != nil && *current.MFASecret != *secret)
	if changed {
		values["mfa_secret_version"] = gorm.Expr("mfa_secret_version + 1")
		values["mfa_last_step"] = nil
	}
	if bumpAuthVersion {
		values["auth_version"] = gorm.Expr("auth_version + 1")
	}
	return tx.Table("admin_user").Where("id = ? AND deleted_at IS NULL", id).Updates(values).Error
}

// consumeAdminTOTPStep 在调用方持有账号行锁的事务内消费 account 当前密钥代际的 step。
// step 为 VerifyTOTP 的成功结果；拒绝旧步或重复步。错误由调用方回滚安全状态及审计。
// ctx 沿用 tx；函数不提交事务、不写日志或返回密钥；已消费步返回 ErrUsedTOTP，版本失配返回验证码错误。
func consumeAdminTOTPStep(tx *gorm.DB, account AdminAccount, step int64) error {
	if account.MFALastStep != nil && step <= *account.MFALastStep {
		return operations.ErrUsedTOTP
	}
	res := tx.Table("admin_user").Where("id=? AND mfa_secret_version=? AND (mfa_last_step IS NULL OR mfa_last_step<?)", account.ID, account.MFASecretVersion, step).
		Update("mfa_last_step", step)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return operations.ErrInvalidTOTP
	}
	return nil
}

// LockActiveAdmin 锁读启用账号及认证字段；调用方须比对会话的 AuthVersion，不得将密码散列或密钥返回 HTTP 或写入审计。
// status 字面量对应 admin_user.status 词表中的 active，查询不因类型化拆分。
func (s AccountRepo) LockActiveAdmin(tx *gorm.DB, id uint64) (AdminAccount, error) {
	var row AdminAccount
	err := tx.Table("admin_user").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND status='"+string(AdminUserActive)+"' AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}
