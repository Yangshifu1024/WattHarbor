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

// accountUsernamePattern 保留3–64位英文、数字、下划线、点和连字符的登录名约束。
var accountUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)

// AccountCreateInput 保留原新建账号字段，口令只参与散列，不作为审计快照。
type AccountCreateInput struct {
	Username    string `json:"username"`     // 原登录名
	DisplayName string `json:"display_name"` // 最多64字符的显示名
	Password    string `json:"password"`     // 初始12–72字节口令
	RoleID      uint64 `json:"role_id"`      // 必须存在且可授予的原角色
	Phone       string `json:"phone"`        // 最多32字节的联系号码
}

// AccountUpdateInput 用指针保留缺省与显式空值，角色及状态敏感变更撤销旧会话。
type AccountUpdateInput struct {
	DisplayName *string `json:"display_name"` // 最多128字符，空字符串清空
	Phone       *string `json:"phone"`        // 最多32字节，空字符串清空
	Email       *string `json:"email"`        // 最多128字节，非空须包含@
	RoleID      *uint64 `json:"role_id"`      // 可授予角色，缺省不修改
	Status      *string `json:"status"`       // active／disabled，缺省不修改
}

// Validate 核对in原公开字段组合，返回输入错误，无IO或数据库列映射。
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

// ManagedMFAInput 保留原目标账号MFA输入；确认密钥须匹配服务端原登记，不允许直接选择替代密钥。
type ManagedMFAInput struct {
	Action string `json:"action"` // enrol／confirm／disable原动作
	Secret string `json:"secret"` // 确认时回传原登记材料
	Code   string `json:"code"`   // 确认时原TOTP证明
}

// ManagedMFAReply 按原动作返回登记材料或启用事实，HTTP保持原响应形状。
type ManagedMFAReply struct {
	ID      uint64 // 原目标账号，仅确认／关闭输出
	Secret  string // 服务端新登记材料，仅登记输出
	URI     string // 原目标登录名的验证器URI，仅登记输出
	Code    string // 原管理登记响应的当前验证码，不写审计
	Enabled bool   // 变更后的启用事实
}

// AccountService 拥有账号创建、更新、删除、解锁、重置及管理MFA的完整准入／审计事务。
type AccountService struct {
	DB *gorm.DB /* 所属账号数据库，不复制到HTTP */
}

// List 在ctx下核对actor当前read权限，按query返回原列表或输入／授权／存储错误，只读。
func (s AccountService) List(ctx context.Context, actor operations.Profile, query persist.PageQuery) (persist.Page[AdminUserRow], error) {
	if query.Page < 1 || query.Page > 1000000 || query.PageSize < 1 || query.PageSize > 100 || utf8.RuneCountInString(query.Keyword) > 128 || (query.Status != "" && query.Status != "active" && query.Status != "disabled" && query.Status != "locked") {
		return persist.Page[AdminUserRow]{}, &ManagementInputError{"关键词过长或状态无效"}
	}
	if _, err := readManagementActor(ctx, s.DB, actor, "admin_user.read"); err != nil {
		return persist.Page[AdminUserRow]{}, err
	}
	return (AccountRepo{DB: s.DB}).ListAdminUsers(ctx, query)
}

// Create 在ctx下核对audit当前create权限及input角色集合，账号与安全审计同事务，返回ID或错误。
// 散列在锁前生成，口令、散列及MFA材料不进入新建审计；原用户名唯一键仍仲裁重复请求。
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
			Username string `json:"username"` // 原新建登录名，不保存口令
			RoleID   uint64 `json:"role_id"`  // 实际授予角色，不保存认证材料
		}{input.Username, input.RoleID}
		return AppendAuditInTx(tx, audit, AuditChange{Action: "create", Target: "admin_user", ID: id, After: after})
	})
	return id, err
}

// Update 在ctx下以audit当前update权限修改id的input，返回是否撤销会话或输入／状态／存储错误。
// 同角色回传不提升版本；真实角色／状态变化提升版本，最后有效客户管理员不得被停用或改派。
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

// Delete 在ctx下以audit当前delete权限软删除id，保留最后管理员和本人保护；状态／版本／审计同事务。
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

// Unlock 在ctx下以audit当前update权限恢复id、清空计数和锁定，状态变更撤销旧会话；错误整体回滚。
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

// ResetPassword 在ctx下以audit当前重置权限修改id的password，保留停用但解除临时锁定，提升版本并审计。
// 口令及散列不进审计；散列在锁前生成，输入／授权／存储错误不写入任何认证事实。
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

// MFA 在ctx下以audit当前update权限为id执行input，返回原管理回执或本人／证明／状态／存储错误。
// 原管理登记响应仍含服务端材料，审计仅保存安全快照；确认必须匹配锁后原登记，不接纳自选密钥。
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
