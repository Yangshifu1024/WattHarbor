package ops

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 后台账号与角色的管理读写。登录、改密与 MFA 校验在 account_repo.go，
// 这里承接列表、创建、编辑、停用、软删除与角色可授予性判断。
//
// auth_version 在Redis原会话及实时档案中比对，敏感变更后旧签名不能恢复有效身份。

// AdminUserRow 是 PC 后台管理员账号（admin_user 表）的列表行映射。
// 账号归属 central_db，与终端用户 central_db.user 分开存放；一个账号一个主角色。
// 可空字段一律用指针：角色被软删除与"角色 ID 为 0"在响应里是两回事。
type AdminUserRow struct {
	ID            uint64  `json:"id" gorm:"column:id"`                                 // 账号主键
	Username      string  `json:"username" gorm:"column:username"`                     // 登录用户名，全局唯一
	DisplayName   *string `json:"display_name" gorm:"column:display_name"`             // 显示名称；nil 表示未设置
	RoleID        *uint64 `json:"role_id" gorm:"column:role_id"`                       // 主角色 ID；nil 表示尚未分配角色
	RoleCode      *string `json:"role_code" gorm:"column:role_code"`                   // 主角色编码，由 role 表联查；角色已删时为 nil
	RoleName      *string `json:"role_name" gorm:"column:role_name"`                   // 角色名称；角色已删时为空
	Phone         *string `json:"phone" gorm:"column:phone"`                           // 备用手机号；nil 表示未登记
	Email         *string `json:"email" gorm:"column:email"`                           // 邮箱；nil 表示未登记
	Status        string  `json:"status" gorm:"column:status"`                         // active 正常 / disabled 停用 / locked 连续登录失败临时锁定
	MFAEnabled    bool    `json:"mfa_enabled" gorm:"column:mfa_enabled"`               // 是否已启用双因素认证
	LastLoginAt   *string `json:"last_login_at" gorm:"column:last_login_at"`           // 最近成功登录时刻，未登录为空
	LockedUntil   *string `json:"locked_until" gorm:"column:locked_until"`             // 临时锁定截止，未锁定为空
	FailedLogins  uint32  `json:"failed_login_count" gorm:"column:failed_login_count"` // 连续登录失败次数
	DeletePending bool    `json:"-" gorm:"-"`                                          // 是否有待处理的下线流程，本列表不使用、不输出
}

const adminUserSelect = "u.id,u.username,u.display_name,u.role_id," +
	"r.code AS role_code,r.name AS role_name,u.phone,u.email,u.status," +
	"u.mfa_enabled,u.last_login_at,u.locked_until,u.failed_login_count"

// ListAdminUsers 分页返回未删除的后台账号，可按状态与用户名/姓名关键字过滤。
// ctx传递取消，q保留分页与状态；关键词通过LikePattern转义通配符，返回原分页或存储错误，不写入。
func (s AccountRepo) ListAdminUsers(ctx context.Context, q persist.PageQuery) (persist.Page[AdminUserRow], error) {
	out := persist.Page[AdminUserRow]{Items: []AdminUserRow{}, Page: q.Page, PageSize: q.PageSize}
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

// LockCustomerAdminRole 在tx下锁定有效customer_admin角色，返回缺失／存储错误，不写入。
// 该行是账号分配、角色变更及最后管理员保护的共同串行点；必须先于任何目标账号锁。
// 行锁只串行事务，不刷新旧RR快照，因此后续权限及剩余数量仍须显式当前读。
func (s AccountRepo) LockCustomerAdminRole(tx *gorm.DB) error {
	var row struct {
		ID uint64 /* 内置角色串行点主键 */
	}
	return tx.Table("role").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("code = ? AND deleted_at IS NULL", "customer_admin").Take(&row).Error
}

// CountOtherActiveAdminsInTx 在ctx与tx下当前统计除id外有效客户管理员，返回数量或存储错误，不修改。
// 调用方先持LockCustomerAdminRole；UPDATE当前读避免旧RR漏掉另一事务刚提交的停用／删除。
func (s AccountRepo) CountOtherActiveAdminsInTx(ctx context.Context, tx *gorm.DB, id uint64) (int64, error) {
	var remaining int64
	err := tx.WithContext(ctx).Table("admin_user AS u").
		Joins("JOIN role AS r ON r.id = u.role_id").
		Where("u.deleted_at IS NULL AND u.status = 'active' AND r.code = 'customer_admin' AND r.deleted_at IS NULL").
		Where("u.id <> ?", id).Clauses(clause.Locking{Strength: "UPDATE"}).Count(&remaining).Error
	return remaining, err
}

// AdminUserSnapshot 是账号管理的安全原快照，显式排除密码散列及MFA密钥，列映射仅位于所属repo。
type AdminUserSnapshot struct {
	ID               uint64     `json:"id"`                 // 原账号主键
	Username         string     `json:"username"`           // 原登录名
	DisplayName      *string    `json:"display_name"`       // 原可空显示名
	RoleID           *uint64    `json:"role_id"`            // 原可空角色，不使用浮点身份转换
	Phone            *string    `json:"phone"`              // 原联系号码
	Email            *string    `json:"email"`              // 原可空邮箱
	Status           string     `json:"status"`             // 原有效／停用／锁定状态
	MFAEnabled       bool       `json:"mfa_enabled"`        // 原启用事实，不含密钥
	LastLoginAt      *time.Time `json:"last_login_at"`      // 原可空登录时刻
	LockedUntil      *time.Time `json:"locked_until"`       // 原可空锁定截止
	FailedLoginCount uint32     `json:"failed_login_count"` // 原失败计数
	AuthVersion      uint64     `json:"auth_version"`       // 原权限版本，用于验证敏感变更
	DeletedAt        *time.Time `json:"deleted_at"`         // 原可空删除时刻
	DeletedBy        *uint64    `json:"deleted_by"`         // 原可空删除操作人
}

// LockAdminUser 在tx下当前锁读id未删除账号的安全快照，返回原字段或缺失／存储错误，不读取凭据材料。
func (s AccountRepo) LockAdminUser(tx *gorm.DB, id uint64) (AdminUserSnapshot, error) {
	var before AdminUserSnapshot
	err := tx.Table("admin_user").Select("id,username,display_name,role_id,phone,email,status,mfa_enabled,last_login_at,locked_until,failed_login_count,auth_version,deleted_at,deleted_by").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error
	return before, err
}

// InsertAdminUser 落一个后台账号，返回新账号 ID。密码散列由调用方算好传入。
func (s AccountRepo) InsertAdminUser(tx *gorm.DB, values map[string]any) (uint64, error) {
	if err := tx.Table("admin_user").Create(values).Error; err != nil {
		return 0, err
	}
	return persist.LastInsertID(tx)
}

// CreateManagementUser 在tx下将input和hash映射到所属账号列，返回自增ID或写入错误，不记录口令明文。
func (s AccountRepo) CreateManagementUser(tx *gorm.DB, input AccountCreateInput, hash string) (uint64, error) {
	return s.InsertAdminUser(tx, map[string]any{"username": input.Username, "display_name": input.DisplayName, "password_hash": hash, "phone": input.Phone, "role_id": input.RoleID, "status": "active"})
}

// UpdateManagementUser 在tx下映射id的input列；roleChanged控制角色写入，revoke控制权限版本提升。
// 返回存储错误，不允许调用方输入任意列或认证版本。
func (s AccountRepo) UpdateManagementUser(tx *gorm.DB, id uint64, input AccountUpdateInput, roleChanged, revoke bool) error {
	values := map[string]any{}
	for key, value := range map[string]*string{"display_name": input.DisplayName, "phone": input.Phone, "email": input.Email, "status": input.Status} {
		if value != nil {
			values[key] = *value
		}
	}
	if roleChanged {
		values["role_id"] = *input.RoleID
	}
	return UpdateAdminUser(tx, id, values, revoke)
}

// UnlockManagementUser 在tx下恢复id并清空计数／锁定，revoke决定是否提升版本，返回存储错误。
func (s AccountRepo) UnlockManagementUser(tx *gorm.DB, id uint64, revoke bool) error {
	return UpdateAdminUser(tx, id, map[string]any{"status": "active", "locked_until": nil, "failed_login_count": 0}, revoke)
}

// ResetManagementPassword 在tx下写id的hash、提升版本并清空计数／锁定；unlock仅解除临时锁定，不启用停用账号。
// 返回存储错误，不接收明文口令，也不自行提交事务。
func (s AccountRepo) ResetManagementPassword(tx *gorm.DB, id uint64, hash string, unlock bool) error {
	values := map[string]any{"password_hash": hash, "failed_login_count": 0, "locked_until": nil}
	if unlock {
		values["status"] = "active"
	}
	return UpdateAdminUser(tx, id, values, true)
}

// LockManagementAccount 在tx下当前锁读id未删除账号的私有认证材料，返回身份或缺失／存储错误。
// 仅用于管理MFA核对原登记，不可作为审计或HTTP返回值。
func (s AccountRepo) LockManagementAccount(tx *gorm.DB, id uint64) (AdminAccount, error) {
	var account AdminAccount
	err := tx.Table("admin_user").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&account).Error
	return account, err
}

// UpdateAdminUser 在tx下将所属内部values写入id，bumpAuthVersion决定提升版本，返回存储错误。
// values只由本repo的具名方法组装，不接收HTTP动态列；修改该映射补版本，调用方持账号锁并同事务审计。
func UpdateAdminUser(tx *gorm.DB, id uint64, values map[string]any, bumpAuthVersion bool) error {
	if bumpAuthVersion {
		values["auth_version"] = gorm.Expr("auth_version + 1")
	}
	return tx.Table("admin_user").Where("id = ?", id).Updates(values).Error
}

// SoftDeleteAdminUser 在tx下将id标记为operatorID于at删除，停用并提升版本，返回存储错误。
// 调用方先完成当前准入及最后管理员保护，审计同事务；保留原行用于历史审计追溯。
func SoftDeleteAdminUser(tx *gorm.DB, id, operatorID uint64, at time.Time) error {
	return tx.Table("admin_user").Where("id = ?", id).Updates(map[string]any{
		"deleted_at": at, "deleted_by": operatorID,
		"status": "disabled", "auth_version": gorm.Expr("auth_version + 1"),
	}).Error
}

// UpdateAdminMFASecret 在tx下写id的secret及enabled，bumpAuthVersion决定撤销旧会话，返回存储错误。
// secret为nil清空材料；调用方当前锁读、核对证明并同事务审计，首次未启用登记不提升版本。
func UpdateAdminMFASecret(tx *gorm.DB, id uint64, secret *string, enabled bool, bumpAuthVersion bool) error {
	values := map[string]any{"mfa_secret": secret, "mfa_enabled": enabled}
	if bumpAuthVersion {
		values["auth_version"] = gorm.Expr("auth_version + 1")
	}
	return tx.Table("admin_user").Where("id = ? AND deleted_at IS NULL", id).Updates(values).Error
}

// LockActiveAdmin 在tx下当前锁读id启用且未删除的账号，返回私有认证材料或缺失／存储错误。
// 调用方比对原AuthVersion后才能变更MFA或管理事实；材料不能作为HTTP响应或审计快照。
func (s AccountRepo) LockActiveAdmin(tx *gorm.DB, id uint64) (AdminAccount, error) {
	var row AdminAccount
	err := tx.Table("admin_user").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND status='active' AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}
