package http

import (
	"context"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// AdminUserRow 是后台账号的列表行，定义在持久化层。
type AdminUserRow = ops.AdminUserRow

// adminUserID 把行锁读回的快照列转成可空主键：列不存在时保留 nil，
// 不能用 0 顶替——"没有角色"和"角色 ID 为 0"在响应里不是一回事。
func adminUserID(value any) *uint64 {
	switch v := value.(type) {
	case int64:
		id := uint64(v)
		return &id
	case uint64:
		id := v
		return &id
	case float64:
		id := uint64(v)
		return &id
	}
	return nil
}

// usernamePattern 是登录用户名的格式约束：字母、数字、下划线、点、短横，3–64 位。
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)

// registerAdminUsers 注册管理员账号及安全操作接口。
// 资料、角色、解锁和 MFA 使用 admin_user.update；重置密码与删除分别要求独立权限。
func (a AdminAccounts) Register(r *gin.Engine) {
	r.GET("/api/v1/admin/admin-users", a.Auth.Require("admin_user.read"), a.listAdminUsers)
	r.PUT("/api/v1/admin/admin-users/:id", a.Auth.Require("admin_user.update"), a.updateAdminUser)
	r.DELETE("/api/v1/admin/admin-users/:id", a.Auth.Require("admin_user.delete"), a.deleteAdminUser)
	r.POST("/api/v1/admin/admin-users/:id/unlock", a.Auth.Require("admin_user.update"), a.unlockAdminUser)
	r.POST("/api/v1/admin/admin-users/:id/reset-password", a.Auth.Require("admin_user.reset_password"), a.resetAdminPassword)
	r.POST("/api/v1/admin/admin-users/:id/mfa", a.Auth.Require("admin_user.update"), a.adminUserMFA)
}

// listAdminUsers 分页返回管理员账号列表，支持按状态（active / disabled / locked）和用户名/显示名关键词过滤，
// 并联查角色编码。本接口只读，不暴露密码哈希、MFA 密钥等敏感字段。
func (a AdminAccounts) listAdminUsers(c *gin.Context) {
	page, ok := ReadAdminPage(c, "active disabled locked")
	if !ok {
		return
	}
	out, err := (ops.AdminStore{DB: a.DB}).ListAdminUsers(c.Request.Context(), persist.PageQuery{
		Page: page.Page, PageSize: page.PageSize, Status: page.Status, Keyword: page.Keyword,
	})
	if err != nil {
		AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// assignableRole 要求角色存在、权限非空，且其全部权限属于操作者已持有的权限。
func assignableRole(ctx context.Context, db *gorm.DB, p operations.Profile, roleID uint64) error {
	codes, err := (ops.AdminStore{DB: db}).AssignableRoleCodes(ctx, db, roleID)
	if err != nil {
		return err
	}
	if len(codes) == 0 {
		return ErrConflict
	}
	for _, code := range codes {
		if !p.HasPermission(code) {
			return errInsufficient
		}
	}
	return nil
}

// errInsufficient 表示要授予的权限超出了操作者自己拥有的范围，由 assignableRole 判定。
var errInsufficient = errors.New("不能授予自己未拥有的权限")

// errLastAdmin 表示这次删除会让系统里一个有效的客户管理员都不剩。
// 它是业务拒绝而非存储故障，翻译成 409 而不是 503。
var errLastAdmin = errors.New("必须保留至少一个有效的客户管理员账号")

// updateAdminUser 更新已提交的资料字段、角色和状态；指针字段区分缺省值与显式更新。
// 状态仅允许 active 或 disabled；禁止操作者停用或降级自己。角色变更递增 auth_version，撤销已有会话。
func (a AdminAccounts) updateAdminUser(c *gin.Context) {
	id, ok := AdminPathID(c)
	if !ok {
		return
	}
	var in struct {
		DisplayName *string `json:"display_name"`
		Phone       *string `json:"phone"`
		Email       *string `json:"email"`
		RoleID      *uint64 `json:"role_id"`
		Status      *string `json:"status"`
	}
	if !DecodeResource(c, &in) {
		return
	}
	if in.DisplayName != nil && utf8.RuneCountInString(*in.DisplayName) > 128 {
		httpapi.BadRequest(c, "显示名称过长")
		return
	}
	if in.Phone != nil && len(*in.Phone) > 32 {
		httpapi.BadRequest(c, "手机号过长")
		return
	}
	if in.Email != nil && (len(*in.Email) > 128 || (*in.Email != "" && !strings.Contains(*in.Email, "@"))) {
		httpapi.BadRequest(c, "邮箱格式无效")
		return
	}
	if in.Status != nil && !AdminOneOf(*in.Status, "active disabled") {
		httpapi.BadRequest(c, "账号状态无效")
		return
	}
	profile := c.MustGet("admin_profile").(operations.Profile)
	values := map[string]any{}
	for key, value := range map[string]*string{"display_name": in.DisplayName, "phone": in.Phone, "email": in.Email, "status": in.Status} {
		if value != nil {
			values[key] = *value
		}
	}
	if len(values) == 0 && in.RoleID == nil {
		httpapi.BadRequest(c, "没有可更新的字段")
		return
	}
	var before ops.AdminUserRow
	roleChanged := false
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		store := ops.AdminStore{DB: tx}
		snapshot, err := store.LockAdminUser(tx, id)
		if err != nil {
			return err
		}
		before.ID, before.RoleID = snapshot["id"].(uint64), adminUserID(snapshot["role_id"])
		// 表单会回传原角色；用有类型的快照比较，避免 uint64 与驱动 int64 被误判为不同。
		roleChanged = in.RoleID != nil && (before.RoleID == nil || *in.RoleID != *before.RoleID)
		if id == profile.ID {
			// 禁止操作者停用或降级自己，避免失去管理入口。
			if in.Status != nil && *in.Status == "disabled" {
				return ErrConflict
			}
			if roleChanged {
				return ErrConflict
			}
		}
		if roleChanged {
			if err := assignableRole(c.Request.Context(), tx, profile, *in.RoleID); err != nil {
				return err
			}
			values["role_id"] = *in.RoleID
		}
		// 仅角色变化时递增凭证版本；普通资料更新保留现有会话。
		if err := ops.UpdateAdminUser(tx, id, values, roleChanged); err != nil {
			return err
		}
		return AdminResourceAudit(tx, profile, "update", "admin_user", id, before, in, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		a.roleFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "sessions_revoked": roleChanged})
}

// roleFailure 角色相关失败的出口：越权授予返回 403，其余走通用处理。
func (a AdminAccounts) roleFailure(c *gin.Context, err error) {
	if errors.Is(err, errInsufficient) {
		httpapi.Write(c, http.StatusForbidden, 1003, "不能授予自己未拥有的权限", nil)
		return
	}
	AdminResourceFailure(c, err)
}

// deleteAdminUser 软删除管理员账号（离职场景，审计要留痕，不做物理删除）。
// 两条硬约束：不能删自己；删完之后系统里必须还剩至少一个 active 的客户管理员账号。
// 删除同时把状态置为 disabled 并让 auth_version + 1，已签发的会话随即失效。
func (a AdminAccounts) deleteAdminUser(c *gin.Context) {
	id, ok := AdminPathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(operations.Profile)
	if id == profile.ID {
		httpapi.Write(c, http.StatusConflict, 2009, "不能删除当前登录账号", nil)
		return
	}
	// 「至少留一个」必须在事务里、在角色行的锁下判断。
	// 放在事务外的话，两个管理员并发互删会各自数到「还剩 1 个」而双双通过。
	store := ops.AdminStore{DB: a.DB}
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := store.LockCustomerAdminRole(tx); err != nil {
			return err
		}
		remaining, err := store.CountOtherActiveAdminsInTx(c.Request.Context(), tx, id)
		if err != nil {
			return err
		}
		if remaining == 0 {
			return errLastAdmin
		}
		before, err := store.LockAdminUser(tx, id)
		if err != nil {
			return err
		}
		if err := ops.SoftDeleteAdminUser(tx, id, profile.ID, time.Now().UTC()); err != nil {
			return err
		}
		return AdminResourceAudit(tx, profile, "delete", "admin_user", id, before, nil, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if errors.Is(err, errLastAdmin) {
		httpapi.Write(c, http.StatusConflict, 2009, "必须保留至少一个有效的客户管理员账号", nil)
		return
	}
	if err != nil {
		AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "deleted": true})
}

// unlockAdminUser 解锁一个因连续登录失败被临时锁定的账号：
// 状态恢复 active、清空锁定到期时间、失败计数归零。
func (a AdminAccounts) unlockAdminUser(c *gin.Context) {
	id, ok := AdminPathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(operations.Profile)
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		before, err := (ops.AdminStore{DB: a.DB}).LockAdminUser(tx, id)
		if err != nil {
			return err
		}
		if err := ops.UpdateAdminUser(tx, id, map[string]any{
			"status": "active", "locked_until": nil, "failed_login_count": 0,
		}, false); err != nil {
			return err
		}
		return AdminResourceAudit(tx, profile, "unlock", "admin_user", id, before, nil, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "unlocked": true})
}

// resetAdminPassword 由管理员为账号重置密码，密码须为 12–72 字节，存 bcrypt 哈希。
// 重置同时让 auth_version + 1，该账号所有已签发会话立即失效，并清空锁定状态与失败计数。
func (a AdminAccounts) resetAdminPassword(c *gin.Context) {
	id, ok := AdminPathID(c)
	if !ok {
		return
	}
	var in struct {
		NewPassword string `json:"new_password"`
	}
	if !DecodeResource(c, &in) {
		return
	}
	if len(in.NewPassword) < 12 || len(in.NewPassword) > 72 {
		httpapi.BadRequest(c, "新密码须为 12–72 字节")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		AdminResourceFailure(c, err)
		return
	}
	profile := c.MustGet("admin_profile").(operations.Profile)
	err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		before, err := (ops.AdminStore{DB: a.DB}).LockAdminUser(tx, id)
		if err != nil {
			return err
		}
		if err := ops.UpdateAdminUser(tx, id, map[string]any{
			"password_hash": string(hash), "failed_login_count": 0, "locked_until": nil,
		}, true); err != nil {
			return err
		}
		return AdminResourceAudit(tx, profile, "reset_password", "admin_user", id, before, nil, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		AdminResourceFailure(c, err)
		return
	}
	// auth_version 加一本身就已经让所有已签发的会话失效了。
	httpapi.OK(c, gin.H{"id": id, "sessions_revoked": true})
}

// adminUserMFA 按 action 管理目标账号的 TOTP：enrol 登记未启用密钥，
// confirm 验证口令后启用，disable 清除密钥。成功操作递增 auth_version。
// 本人 MFA 必须通过个人安全设置操作，禁止在此接口修改自己的验证器。
func (a AdminAccounts) adminUserMFA(c *gin.Context) {
	id, ok := AdminPathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(operations.Profile)
	if id == profile.ID {
		httpapi.Write(c, http.StatusConflict, 2009, "请使用本人账号的安全设置修改双因素认证", nil)
		return
	}
	// 验证器标签使用目标账号身份，确保密钥绑定到被管理的账号。
	targetName, err := (ops.AdminStore{DB: a.DB}).FindAdminUsername(c.Request.Context(), a.DB, id)
	if err != nil {
		AdminResourceFailure(c, err)
		return
	}
	var in struct {
		Action string `json:"action"`
		Secret string `json:"secret"`
		Code   string `json:"code"`
	}
	if !DecodeResource(c, &in) {
		return
	}
	now := time.Now().UTC()
	switch in.Action {
	case "enrol":
		secret, err := auth.NewTOTPSecret()
		if err != nil {
			AdminResourceFailure(c, err)
			return
		}
		uri, err := auth.TOTPURI("ChargePilot", targetName, secret)
		if err != nil {
			AdminResourceFailure(c, err)
			return
		}
		code, err := auth.TOTPCode(secret, now)
		if err != nil {
			AdminResourceFailure(c, err)
			return
		}
		// 登记阶段保存未启用密钥；confirm 验证成功后启用。
		if err := ops.UpdateAdminMFAStaged(a.DB.WithContext(c.Request.Context()), id, secret); err != nil {
			AdminResourceFailure(c, err)
			return
		}
		httpapi.OK(c, gin.H{"secret": secret, "otpauth_uri": uri, "code": code, "enabled": false})
	case "confirm":
		if in.Secret == "" || in.Code == "" {
			httpapi.BadRequest(c, "请提供密钥和验证码")
			return
		}
		if err := auth.VerifyTOTP(in.Secret, in.Code, now); err != nil {
			httpapi.Write(c, http.StatusBadRequest, 1002, "验证码校验失败", nil)
			return
		}
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			before, err := (ops.AdminStore{DB: tx}).LockAdminUser(tx, id)
			if err != nil {
				return err
			}
			if err := ops.UpdateAdminMFASecret(tx, id, &in.Secret, true, true); err != nil {
				return err
			}
			return AdminResourceAudit(tx, profile, "mfa_enable", "admin_user", id, before, nil, c.ClientIP(), c.GetHeader("X-Request-ID"))
		})
		if err != nil {
			AdminResourceFailure(c, err)
			return
		}
		httpapi.OK(c, gin.H{"id": id, "mfa_enabled": true})
	case "disable":
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			before, err := (ops.AdminStore{DB: tx}).LockAdminUser(tx, id)
			if err != nil {
				return err
			}
			if err := ops.UpdateAdminMFASecret(tx, id, nil, false, true); err != nil {
				return err
			}
			return AdminResourceAudit(tx, profile, "mfa_disable", "admin_user", id, before, nil, c.ClientIP(), c.GetHeader("X-Request-ID"))
		})
		if err != nil {
			AdminResourceFailure(c, err)
			return
		}
		httpapi.OK(c, gin.H{"id": id, "mfa_enabled": false})
	default:
		httpapi.BadRequest(c, "请指定 enrol、confirm 或 disable")
	}
}
