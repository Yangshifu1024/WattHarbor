package ops

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/platform/auth"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// AdminAccount 是登录校验所需的后台账号记录，密码和 MFA 等敏感字段不参与 JSON 输出。
type AdminAccount struct {
	AuthVersion      uint64         `json:"-"`             // 凭证版本号：改密或强制下线时自增，用于让已签发的令牌立即失效
	ID               uint64         `json:"admin_user_id"` // 账号主键
	Username         string         `json:"username"`      // 登录名
	DisplayName      sql.NullString `json:"-"`             // 可空显示名，Profile 读取时将 NULL 转为空字符串
	PasswordHash     string         `json:"-"`             // bcrypt 口令散列，绝不出接口
	RoleID           uint64         `json:"role_id"`       // 所属角色 ID，权限由此推导
	Status           string         `json:"-"`             // 账号状态：active / disabled / locked
	MFASecret        *string        `json:"-"`             // TOTP 密钥；指针，未绑定时为 null
	MFAEnabled       bool           `json:"-"`             // 是否已启用双因素认证
	MFASecretVersion uint64         `json:"-"`             // 密钥代际，仅更换或清除密钥时增加，独立于认证版本
	MFALastStep      *int64         `json:"-"`             // 本密钥最后消费的 30 秒时间步，未消费为空
	FailedLoginCount uint32         `json:"-"`             // 连续登录失败次数，达到 5 次锁定
	LockedUntil      sql.NullTime   `json:"-"`             // 锁定到期时间；可空，未锁定为 null
}

// TableName 将 AdminAccount 映射到 admin_user。
func (AdminAccount) TableName() string { return "admin_user" }

// AccountRepo 访问后台账号和认证记录，密码散列和 MFA 密钥仅用于身份验证。
type AccountRepo struct {
	DB *gorm.DB /* 所属账号与审计数据库 */
}

// Bootstrap 仅在没有未删除账号时创建管理员，不覆盖现有账号密码。
// 用户名和密码都为空时跳过；只填一项或密码不在 12–72 字节时拒绝。
func (s AccountRepo) Bootstrap(ctx context.Context, username, password string) error {
	if username == "" && password == "" {
		return nil
	}
	if username == "" || len(username) > 64 || len(password) < 12 || len(password) > 72 {
		return errors.New("admin bootstrap requires username and 12–72 byte password")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var role struct {
			ID uint64 /* 初始化角色的主键 */
		}
		if err := tx.Table("role").Clauses(clause.Locking{Strength: "UPDATE"}).Where("active_code = ?", "customer_admin").Take(&role).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&AdminAccount{}).Where("deleted_at IS NULL").Count(&count).Error; err != nil {
			return err
		}
		// 仅在空安装中初始化管理员账号，不覆盖已有口令。
		if count > 0 {
			return nil
		}
		account := AdminAccount{Username: username, PasswordHash: string(hash), RoleID: role.ID, Status: "active"}
		if err := tx.Create(&account).Error; err != nil {
			return err
		}
		return WriteAuthAudit(tx, account, "bootstrap", "")
	})
}

// Login 校验密码；连续失败 5 次锁定 30 分钟，锁定到期清零失败次数。
// 启用 MFA 时只返回账号，完成二次验证后才记录登录成功；普通登录同时保存时间和审计。
// 用户不存在和密码错误返回同一错误。
func (s AccountRepo) Login(ctx context.Context, username, password, ip string) (AdminAccount, error) {
	var account AdminAccount
	var denied error
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("username = ? AND deleted_at IS NULL", username).Take(&account).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return operations.ErrCredentials
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if account.Status == "disabled" {
			return operations.ErrCredentials
		}
		if account.Status == "locked" && (!account.LockedUntil.Valid || account.LockedUntil.Time.After(now)) {
			return operations.ErrLocked
		}
		attempts := account.FailedLoginCount
		if account.LockedUntil.Valid && !account.LockedUntil.Time.After(now) {
			attempts = 0
		}
		if bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(password)) != nil {
			attempts++
			updates := map[string]any{"failed_login_count": attempts, "status": "active", "locked_until": nil}
			denied = operations.ErrCredentials
			if attempts >= 5 {
				updates["status"] = "locked"
				updates["locked_until"] = now.Add(30 * time.Minute)
				denied = operations.ErrLocked
			}
			if err := tx.Model(&AdminAccount{}).Where("id = ?", account.ID).Updates(updates).Error; err != nil {
				return err
			}
			return WriteAuthAudit(tx, account, "login_failed", ip)
		}
		if account.MFAEnabled {
			// 口令已验证；启用 MFA 的账号需完成二次验证后才记录登录成功并发放会话。
			account.Status = "active"
			return nil
		}
		if err := tx.Model(&AdminAccount{}).Where("id = ?", account.ID).Updates(map[string]any{"failed_login_count": 0, "locked_until": nil, "status": "active", "last_login_at": now}).Error; err != nil {
			return err
		}
		account.Status = "active"
		return WriteAuthAudit(tx, account, "login", ip)
	})
	if err != nil {
		return AdminAccount{}, err
	}
	if denied != nil {
		return AdminAccount{}, denied
	}
	return account, nil
}

// CompleteMFA 锁定账号后核对版本和验证码，成功前原子删除已验证挑战。
// 锁定、版本过期、已消费的正确码或坏码不删除挑战；只有坏码累计失败次数并保存失败审计。
// 删除挑战后 SQL 失败只回滚 SQL，须重新用密码登录。
func (s AccountRepo) CompleteMFA(ctx context.Context, identity operations.AccountIdentity, code, ip string, consume func(context.Context) error) (AdminAccount, error) {
	var account AdminAccount
	var denied error
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", identity.ID).Take(&account).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return operations.ErrCredentials
			}
			return err
		}
		now := time.Now().UTC()
		if account.AuthVersion != identity.AuthVersion || account.Username != identity.Username {
			return operations.ErrInvalidMFAChallenge
		}
		if account.Status == "locked" && (!account.LockedUntil.Valid || account.LockedUntil.Time.After(now)) {
			return operations.ErrLocked
		}
		if account.Status == "disabled" || !account.MFAEnabled || account.MFASecret == nil || *account.MFASecret == "" {
			return operations.ErrCredentials
		}
		step, verifyErr := auth.VerifyTOTP(*account.MFASecret, code, now)
		if verifyErr == nil && account.MFALastStep != nil && step <= *account.MFALastStep {
			return operations.ErrUsedTOTP
		}
		if verifyErr != nil {
			attempts := account.FailedLoginCount + 1
			if account.LockedUntil.Valid && !account.LockedUntil.Time.After(now) {
				attempts = 1
			}
			updates := map[string]any{"failed_login_count": attempts, "status": "active", "locked_until": nil}
			denied = operations.ErrInvalidTOTP
			if attempts >= 5 {
				updates["status"] = "locked"
				updates["locked_until"] = now.Add(30 * time.Minute)
				denied = operations.ErrLocked
			}
			if err := tx.Model(&AdminAccount{}).Where("id = ?", account.ID).Updates(updates).Error; err != nil {
				return err
			}
			return WriteAuthAudit(tx, account, "mfa_failed", ip)
		}
		if consume == nil {
			return operations.ErrInvalidMFAChallenge
		}
		if err := consume(ctx); err != nil {
			return err
		}
		if err := consumeAdminTOTPStep(tx, account, step); err != nil {
			return err
		}
		if err := tx.Model(&AdminAccount{}).Where("id = ?", account.ID).Updates(map[string]any{"failed_login_count": 0, "locked_until": nil, "status": "active", "last_login_at": now}).Error; err != nil {
			return err
		}
		account.Status = "active"
		return WriteAuthAudit(tx, account, "login_mfa", ip)
	})
	if err != nil {
		return AdminAccount{}, err
	}
	if denied != nil {
		return AdminAccount{}, denied
	}
	return account, nil
}

// Profile 只读取 status=active 且未删除的账号和角色，并查询当前权限；无匹配记录返回 ErrCredentials。
func (s AccountRepo) Profile(ctx context.Context, id uint64) (operations.Profile, error) {
	var row struct {
		// AdminAccount 接收账号列，包括角色和认证版本；密码和 MFA 密钥不进入返回资料。
		AdminAccount
		Role     string // 当前有效角色编码
		RoleName string // 当前角色展示名称
	}
	err := s.DB.WithContext(ctx).Table("admin_user AS a").Select("a.*, r.code AS role,r.name AS role_name").Joins("JOIN role AS r ON r.id=a.role_id AND r.deleted_at IS NULL").Where("a.id = ? AND a.status='active' AND a.deleted_at IS NULL", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return operations.Profile{}, operations.ErrCredentials
	}
	if err != nil {
		return operations.Profile{}, err
	}
	p := operations.Profile{AuthVersion: row.AuthVersion, ID: row.ID, Username: row.Username, DisplayName: row.DisplayName.String, RoleID: row.RoleID, Role: row.Role, RoleName: row.RoleName, MFAEnabled: row.MFAEnabled, Permissions: []string{}}
	err = s.DB.WithContext(ctx).Table("permission AS p").Joins("JOIN role_permission AS rp ON rp.permission_id=p.id").Where("rp.role_id = ?", p.RoleID).Order("p.code").Pluck("p.code", &p.Permissions).Error
	p.Permissions = operations.WithoutRetiredPermissions(p.Permissions)
	return p, err
}

// WriteAuthAudit 在账号事务中保存操作和 IP，排除密码、密钥和挑战。
// 审计归属 auth 模块、admin_user 对象，并填写月份分区键 created_month。
func WriteAuthAudit(tx *gorm.DB, a AdminAccount, action, ip string) error {
	now := time.Now().UTC()
	return tx.Table("audit_log").Create(map[string]any{"actor_id": a.ID, "actor_name": a.Username, "module": "auth", "action": action, "target_type": "admin_user", "target_id": fmt.Sprint(a.ID), "client_ip": ip, "created_month": time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}).Error
}

// ChangePassword 验证旧密码后更新新密码并提升 auth_version，使已有令牌失效。
// 新密码须不同于旧密码，长度为 12–72 字节。
func (s AccountRepo) ChangePassword(ctx context.Context, id uint64, oldPassword, newPassword, ip string) error {
	if len(newPassword) < 12 || len(newPassword) > 72 || newPassword == oldPassword {
		return errors.New("new password must differ and contain 12–72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var a AdminAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status='active' AND deleted_at IS NULL", id).Take(&a).Error; err != nil {
			return err
		}
		if bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(oldPassword)) != nil {
			return operations.ErrCredentials
		}
		if err := tx.Model(&AdminAccount{}).Where("id = ?", id).Updates(map[string]any{"password_hash": string(hash), "auth_version": gorm.Expr("auth_version+1")}).Error; err != nil {
			return err
		}
		return WriteAuthAudit(tx, a, "change_password", ip)
	})
}

// Identity 提取会话所需账号 ID、登录名和认证版本；密码散列和 MFA 密钥不进入 Redis 会话。
func (a AdminAccount) Identity() operations.AccountIdentity {
	return operations.AccountIdentity{ID: a.ID, AuthVersion: a.AuthVersion, Username: a.Username}
}
