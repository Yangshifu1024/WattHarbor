package ops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// AdminAccount 是一个后台账号在 admin_user 表上的行，同时承载登录判定所需的全部字段。
// 敏感字段一律 json："-"，不会随任何接口返回。
type AdminAccount struct {
	AuthVersion      uint64         `json:"-"`             // 凭证版本号：改密或强制下线时自增，用于让已签发的令牌立即失效
	ID               uint64         `json:"admin_user_id"` // 账号主键
	Username         string         `json:"username"`      // 登录名
	DisplayName      sql.NullString `json:"-"`             // 显示名；可空，缺失时前端回退到登录名
	PasswordHash     string         `json:"-"`             // bcrypt 口令散列，绝不出接口
	RoleID           uint64         `json:"role_id"`       // 所属角色 ID，权限由此推导
	Status           string         `json:"-"`             // 账号状态：active / disabled / locked
	MFASecret        *string        `json:"-"`             // TOTP 密钥；指针，未绑定时为 null
	MFAEnabled       bool           `json:"-"`             // 是否已启用双因素认证
	FailedLoginCount uint32         `json:"-"`             // 连续登录失败次数，达到 5 次锁定
	LockedUntil      sql.NullTime   `json:"-"`             // 锁定到期时间；可空，未锁定为 null
}

// TableName 指明 AdminAccount 映射到 admin_user（表名与模型名不一致，必须显式指定）。
func (AdminAccount) TableName() string { return "admin_user" }

type AdminStore struct{ DB *gorm.DB }

// Bootstrap 在全新安装时创建第一个后台账号。
// 用户名与密码都为空表示不初始化；用户名与密码只给一半、或密码不在 12–72 字节之间，一律报错。
// 只在库里还没有任何账号时才创建，绝不覆盖已有账号的口令。
func (s AdminStore) Bootstrap(ctx context.Context, username, password string) error {
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
		var role struct{ ID uint64 }
		if err := tx.Table("role").Clauses(clause.Locking{Strength: "UPDATE"}).Where("code = 'customer_admin' AND deleted_at IS NULL").Take(&role).Error; err != nil {
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

// Login 校验用户名与口令，成功时返回账号并写登录时间与审计。
// 连续 5 次失败即锁定 30 分钟，锁定到期后失败计数自动清零。
// 账号启用双因素认证时，口令正确也只返回账号、不写成功状态，由调用方接着走 MFA 流程。
// 用户名不存在与口令错误返回同一个错误，不给暴力破解区分线索。
func (s AdminStore) Login(ctx context.Context, username, password, ip string) (AdminAccount, error) {
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

// CompleteMFA 在ctx下锁identity原账号，核对原版本及code；ip用于审计，consume原子消费已验证挑战。
// 锁定／版本失效不消费；坏码计数与审计同事务提交，好码先消费后记录成功。
// 消费后SQL失败会回滚SQL但不恢复Redis挑战，失败关闭且须重新密码登录。
func (s AdminStore) CompleteMFA(ctx context.Context, identity operations.AccountIdentity, code, ip string, consume func(context.Context) error) (AdminAccount, error) {
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
		if err := auth.VerifyTOTP(*account.MFASecret, code, now); err != nil {
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

// Profile 读取一个账号的登录身份与权限清单：先联表取角色编码，再按角色展开权限码。
// 只接受 active 且未删除的账号；查不到与被停用都返回凭据错误，避免泄漏账号是否存在。
func (s AdminStore) Profile(ctx context.Context, id uint64) (operations.Profile, error) {
	var row struct {
		AdminAccount
		Role     string
		RoleName string
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

// audit 写一条认证类审计（登录成功、失败、MFA、改密、初始化），
// 固定记在 auth 模块和 admin_user 对象上。created_month 是该表按月分区的键，必须一并写入。
func WriteAuthAudit(tx *gorm.DB, a AdminAccount, action, ip string) error {
	now := time.Now().UTC()
	return tx.Table("audit_log").Create(map[string]any{"actor_id": a.ID, "actor_name": a.Username, "module": "auth", "action": action, "target_type": "admin_user", "target_id": fmt.Sprint(a.ID), "client_ip": ip, "created_month": time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}).Error
}

// ChangePassword 校验旧口令后改新口令，并把 auth_version 自增一，
// 使该账号已签发的令牌立即失效。旧口令错误返回凭据错误；
// 新口令必须与旧口令不同且在 12–72 字节之间（bcrypt 的上限）。
func (s AdminStore) ChangePassword(ctx context.Context, id uint64, oldPassword, newPassword, ip string) error {
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

// Identity 把账号收敛为会话需要的最小身份：ID、登录名与凭证版本。
// 口令散列、MFA 密钥等登录判定字段不进入 Redis 会话。
func (a AdminAccount) Identity() operations.AccountIdentity {
	return operations.AccountIdentity{ID: a.ID, AuthVersion: a.AuthVersion, Username: a.Username}
}
