package ops

import (
	"context"
	"crypto/subtle"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/persist"
	"chargepilot/internal/platform/auth"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// accountUsernamePattern 限制登录名为 3–64 位英文、数字、下划线、点或连字符。
var accountUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)

// AccountCreateInput 是新建账号资料，密码只用于生成散列，不保存到审计。
type AccountCreateInput struct {
	Username    string `json:"username"`     // 登录名
	DisplayName string `json:"display_name"` // 最多64字符的显示名
	Password    string `json:"password"`     // 初始12–72字节口令
	RoleID      uint64 `json:"role_id"`      // 必须存在且可授予的角色
	Phone       string `json:"phone"`        // 最多32字节的联系号码
}

// AccountUpdateInput 用指针区分未提交和显式空值；角色或状态变更须撤销旧会话。
type AccountUpdateInput struct {
	DisplayName *string `json:"display_name"` // 最多128字符，空字符串清空
	Phone       *string `json:"phone"`        // 最多32字节，空字符串清空
	Email       *string `json:"email"`        // 最多128字节，非空须包含@
	RoleID      *uint64 `json:"role_id"`      // 可授予角色，缺省不修改
	Status      *string `json:"status"`       // active／disabled，缺省不修改
}

// Validate 检查账号更新字段组合。
func (in AccountUpdateInput) Validate() error {
	if in.DisplayName != nil && utf8.RuneCountInString(*in.DisplayName) > 128 {
		return &ManagementInputError{"显示名称过长"}
	}
	if in.Phone != nil && len(*in.Phone) > 32 {
		return &ManagementInputError{"手机号过长"}
	}
	if in.Email != nil && (len(*in.Email) > 128 || (*in.Email != "" && !strings.Contains(*in.Email, "@"))) {
		return &ManagementInputError{"邮箱格式无效"}
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "disabled" {
		return &ManagementInputError{"账号状态无效"}
	}
	if in.DisplayName == nil && in.Phone == nil && in.Email == nil && in.RoleID == nil && in.Status == nil {
		return &ManagementInputError{"没有可更新的字段"}
	}
	return nil
}

// ManagedMFAInput 是管理员 MFA 操作参数；确认密钥须匹配服务端登记的密钥。
type ManagedMFAInput struct {
	Action string `json:"action"` // enrol／confirm／disable 动作
	Secret string `json:"secret"` // 确认时回传服务端登记材料
	Code   string `json:"code"`   // 确认时 TOTP 证明
}

// ManagedMFAReply 返回 MFA 登记材料或启用结果。
type ManagedMFAReply struct {
	ID      uint64 // 目标账号，仅确认／关闭输出
	Secret  string // 服务端新登记材料，仅登记输出
	URI     string // 目标登录名的验证器 URI，仅登记输出
	Code    string // 管理登记响应的当前验证码，不写审计
	Enabled bool   // 变更后的启用事实
}

// AccountService 负责账号管理的权限校验、变更和审计事务。
type AccountService struct {
	DB *gorm.DB /* 所属账号数据库，不复制到 HTTP */
}

// List 核对账号读取权限后分页查询。
func (s AccountService) List(ctx context.Context, actor operations.Profile, query persist.PageQuery) (persist.Page[AdminUserRow], error) {
	if query.Page < 1 || query.Page > 1000000 || query.PageSize < 1 || query.PageSize > 100 || utf8.RuneCountInString(query.Keyword) > 128 || (query.Status != "" && query.Status != "active" && query.Status != "disabled" && query.Status != "locked") {
		return persist.Page[AdminUserRow]{}, &ManagementInputError{"关键词过长或状态无效"}
	}
	if _, err := readManagementActor(ctx, s.DB, actor, "admin_user.read"); err != nil {
		return persist.Page[AdminUserRow]{}, err
	}
	return (AccountRepo{DB: s.DB}).ListAdminUsers(ctx, query)
}

// Create 核对创建权限和角色可授予范围，账号与审计同事务保存。
// 密码散列在加锁前生成；密码、散列及 MFA 材料不进入审计。
func (s AccountService) Create(ctx context.Context, audit AuditContext, input AccountCreateInput) (uint64, error) {
	if !accountUsernamePattern.MatchString(input.Username) || len(input.Password) < 12 || len(input.Password) > 72 || utf8.RuneCountInString(input.DisplayName) > 64 || len(input.Phone) > 32 || input.RoleID == 0 {
		return 0, &ManagementInputError{"用户名须为 3–64 位英文/数字，密码须为 12–72 字节，并选择有效角色"}
	}
	if s.DB == nil {
		return 0, errors.New("ops management unavailable")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	var id uint64
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, err := lockedManagementActor(tx, audit.Actor, "admin_user.create")
		if err != nil {
			return err
		}
		audit.Actor = actor
		if err := assignManagementRole(tx, actor, input.RoleID, true); err != nil {
			return err
		}
		id, err = (AccountRepo{DB: tx}).CreateManagementUser(tx, input, string(hash))
		if err != nil {
			return err
		}
		after := struct {
			Username string `json:"username"` // 新建登录名，不保存口令
			RoleID   uint64 `json:"role_id"`  // 实际授予角色，不保存认证材料
		}{input.Username, input.RoleID}
		return AppendAuditInTx(tx, audit, AuditChange{Action: "create", Target: "admin_user", ID: id, After: after})
	})
	return id, err
}

// Update 核对更新权限后修改账号；实际角色或状态变化才提升认证版本，撤销旧会话。
// 不能停用或改派最后一个有效客户管理员。
func (s AccountService) Update(ctx context.Context, audit AuditContext, id uint64, input AccountUpdateInput) (bool, error) {
	if err := input.Validate(); err != nil {
		return false, err
	}
	if s.DB == nil {
		return false, errors.New("ops management unavailable")
	}
	revoked := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, err := lockedManagementActor(tx, audit.Actor, "admin_user.update")
		if err != nil {
			return err
		}
		audit.Actor = actor
		store := AccountRepo{DB: tx}
		before, err := store.LockAdminUser(tx, id)
		if err != nil {
			return err
		}
		roleChanged := input.RoleID != nil && (before.RoleID == nil || *input.RoleID != *before.RoleID)
		statusChanged := input.Status != nil && *input.Status != before.Status
		if id == actor.ID && ((input.Status != nil && *input.Status == "disabled") || roleChanged) {
			return operations.ErrConflict
		}
		if roleChanged {
			if err := assignManagementRole(tx, actor, *input.RoleID, false); err != nil {
				return err
			}
		}
		if before.Status == "active" && before.RoleID != nil && (roleChanged || (input.Status != nil && *input.Status == "disabled")) {
			previousRole, err := (RoleRepo{DB: tx}).LockRole(tx, *before.RoleID)
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil && previousRole.Code == "customer_admin" {
				remaining, err := store.CountOtherActiveAdminsInTx(ctx, tx, id)
				if err != nil {
					return err
				}
				if remaining == 0 {
					return ErrLastAdmin
				}
			}
		}
		revoked = roleChanged || statusChanged
		if err := store.UpdateManagementUser(tx, id, input, roleChanged, revoked); err != nil {
			return err
		}
		return AppendAuditInTx(tx, audit, AuditChange{Action: "update", Target: "admin_user", ID: id, Before: before, After: input})
	})
	return revoked, err
}

// Delete 核对删除权限后软删账号，保护本人和最后客户管理员；状态、版本和审计同事务。
func (s AccountService) Delete(ctx context.Context, audit AuditContext, id uint64) error {
	if id == audit.Actor.ID {
		return ErrSelfDelete
	}
	if s.DB == nil {
		return errors.New("ops management unavailable")
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, err := lockedManagementActor(tx, audit.Actor, "admin_user.delete")
		if err != nil {
			return err
		}
		audit.Actor = actor
		store := AccountRepo{DB: tx}
		before, err := store.LockAdminUser(tx, id)
		if err != nil {
			return err
		}
		// 只有移除有效客户管理员才减少保护集合；普通账号删除不依赖无关角色的数量。
		if before.Status == "active" && before.RoleID != nil {
			role, err := (RoleRepo{DB: tx}).LockRole(tx, *before.RoleID)
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil && role.Code == "customer_admin" {
				remaining, err := store.CountOtherActiveAdminsInTx(ctx, tx, id)
				if err != nil {
					return err
				}
				if remaining == 0 {
					return ErrLastAdmin
				}
			}
		}
		if err := SoftDeleteAdminUser(tx, id, actor.ID, time.Now().UTC()); err != nil {
			return err
		}
		return AppendAuditInTx(tx, audit, AuditChange{Action: "delete", Target: "admin_user", ID: id, Before: before})
	})
}

// Unlock 核对更新权限后恢复账号、清空失败次数和锁定时间，状态变更须撤销旧会话。
func (s AccountService) Unlock(ctx context.Context, audit AuditContext, id uint64) error {
	if s.DB == nil {
		return errors.New("ops management unavailable")
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, err := lockedManagementActor(tx, audit.Actor, "admin_user.update")
		if err != nil {
			return err
		}
		audit.Actor = actor
		store := AccountRepo{DB: tx}
		before, err := store.LockAdminUser(tx, id)
		if err != nil {
			return err
		}
		if err := store.UnlockManagementUser(tx, id, before.Status != "active"); err != nil {
			return err
		}
		return AppendAuditInTx(tx, audit, AuditChange{Action: "unlock", Target: "admin_user", ID: id, Before: before})
	})
}

// ResetPassword 核对重置权限后更换密码、提升认证版本并清空临时锁定；停用账号仍保持停用。
// 密码和散列不进入审计，散列在加锁前生成。
func (s AccountService) ResetPassword(ctx context.Context, audit AuditContext, id uint64, password string) error {
	if len(password) < 12 || len(password) > 72 {
		return &ManagementInputError{"新密码须为 12–72 字节"}
	}
	if s.DB == nil {
		return errors.New("ops management unavailable")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, err := lockedManagementActor(tx, audit.Actor, "admin_user.reset_password")
		if err != nil {
			return err
		}
		audit.Actor = actor
		store := AccountRepo{DB: tx}
		before, err := store.LockAdminUser(tx, id)
		if err != nil {
			return err
		}
		if err := store.ResetManagementPassword(tx, id, string(hash), before.Status == "locked"); err != nil {
			return err
		}
		return AppendAuditInTx(tx, audit, AuditChange{Action: "reset_password", Target: "admin_user", ID: id, Before: before})
	})
}

// MFA 核对更新权限后管理账号 MFA，确认密钥须匹配加锁后读取的登记材料。
// 响应可含登记材料，审计仅保存排除敏感字段的快照。
func (s AccountService) MFA(ctx context.Context, audit AuditContext, id uint64, input ManagedMFAInput) (ManagedMFAReply, error) {
	if id == audit.Actor.ID {
		return ManagedMFAReply{}, ErrSelfMFA
	}
	if input.Action != "enrol" && input.Action != "confirm" && input.Action != "disable" {
		return ManagedMFAReply{}, &ManagementInputError{"请指定 enrol、confirm 或 disable"}
	}
	if input.Action == "confirm" && (input.Secret == "" || input.Code == "") {
		return ManagedMFAReply{}, &ManagementInputError{"请提供密钥和验证码"}
	}
	if s.DB == nil {
		return ManagedMFAReply{}, errors.New("ops management unavailable")
	}
	var result ManagedMFAReply
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		actor, err := lockedManagementActor(tx, audit.Actor, "admin_user.update")
		if err != nil {
			return err
		}
		audit.Actor = actor
		store := AccountRepo{DB: tx}
		before, err := store.LockAdminUser(tx, id)
		if err != nil {
			return err
		}
		account, err := store.LockManagementAccount(tx, id)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		action := ""
		switch input.Action {
		case "enrol":
			secret, err := auth.NewTOTPSecret()
			if err != nil {
				return err
			}
			uri, err := auth.TOTPURI("ChargePilot", account.Username, secret)
			if err != nil {
				return err
			}
			code, err := auth.TOTPCode(secret, now)
			if err != nil {
				return err
			}
			if err := UpdateAdminMFASecret(tx, id, &secret, false, account.MFAEnabled); err != nil {
				return err
			}
			result = ManagedMFAReply{Secret: secret, URI: uri, Code: code}
			action = "mfa_enrol"
		case "confirm":
			if account.MFASecret == nil || subtle.ConstantTimeCompare([]byte(*account.MFASecret), []byte(input.Secret)) != 1 || auth.VerifyTOTP(input.Secret, input.Code, now) != nil {
				return &ManagementInputError{"验证码校验失败"}
			}
			if err := UpdateAdminMFASecret(tx, id, account.MFASecret, true, !account.MFAEnabled); err != nil {
				return err
			}
			result = ManagedMFAReply{ID: id, Enabled: true}
			action = "mfa_enable"
		case "disable":
			if err := UpdateAdminMFASecret(tx, id, nil, false, account.MFAEnabled || account.MFASecret != nil); err != nil {
				return err
			}
			result = ManagedMFAReply{ID: id}
			action = "mfa_disable"
		}
		return AppendAuditInTx(tx, audit, AuditChange{Action: action, Target: "admin_user", ID: id, Before: before})
	})
	return result, err
}
