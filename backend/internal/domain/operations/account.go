// Package operations 定义后台账号、权限、数据范围与审计；不依赖数据库或 HTTP 框架。
package operations

import (
	"errors"
	"strings"
)

// 后台登录失败的三类原因。对外只区分"凭据错误"和"被锁定"。
// 避免通过报错文案区分"用户不存在"与"密码错误"，给暴力破解留线索。
var (
	ErrCredentials    = errors.New("用户名或密码错误")
	ErrLocked         = errors.New("账号已锁定，请在 30 分钟后重试")
	ErrMFA            = errors.New("此账号已启用双因素认证，当前登录入口暂不支持")
	ErrInvalidTOTP    = errors.New("验证码无效或已过期")
	ErrInvalidRefresh = errors.New("invalid or expired refresh token")
	// ErrInvalidMFAChallenge 表示登录挑战不存在、已经过期，或已被一次成功的二次验证用掉。
	ErrInvalidMFAChallenge = errors.New("invalid or expired mfa challenge")
)

// Profile 保存鉴权后的管理员标识、权限及审计信息，不包含口令散列。
// 它是会话与审计的共同身份来源：Redis 会话记录 AccountIdentity，审计记录取这里的 ID 与登录名。
type Profile struct {
	AuthVersion uint64   `json:"-"`             // 凭证版本号，与账号一致，用于校验令牌是否已失效
	ID          uint64   `json:"admin_user_id"` // 账号主键
	Username    string   `json:"username"`      // 登录名
	DisplayName string   `json:"display_name"`  // 显示名
	Role        string   `json:"role"`          // 角色编码，如 customer_admin
	RoleName    string   `json:"role_name"`     // 当前角色名称，从 role 表读取，支持自定义角色。
	RoleID      uint64   `json:"role_id"`       // 角色主键
	MFAEnabled  bool     `json:"mfa_enabled"`   // 当前登录账号是否已启用双因素认证。
	Permissions []string `json:"permissions"`   // 权限码列表，按 code 排序；前端用它决定按钮可见性
}

// AccountIdentity 是会话与登录挑战只需要记住的账号最小身份。
// 口令散列等登录判定字段不进入会话，避免在 Redis 中留存凭证材料。
type AccountIdentity struct {
	ID          uint64 // 账号主键
	AuthVersion uint64 // 凭证版本号，与账号一致；改密或角色调整后自增，使旧会话失效
	Username    string // 登录名，供审计记录操作人
}

// 允许的账号状态。disabled 为人工停用，locked 为连续登录失败触发的临时锁定。
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusLocked   = "locked"
)

// 连续登录失败达到该次数即锁定账号。
const MaxFailedLogins = 5

// LockDuration 是账号被锁定后的持续时间。
const LockDuration = 30 // 分钟

// WithoutRetiredPermissions 让未重建开发库中的已移除功能权限不再参与展示或授权。
// 角色及其余权限保持原样，不修改已有权限绑定数据。
func WithoutRetiredPermissions(codes []string) []string {
	result := make([]string, 0, len(codes))
	for _, code := range codes {
		if !RetiredPermission(code) {
			result = append(result, code)
		}
	}
	return result
}

// RetiredPermission 列出已下线功能的权限码。这些功能已从路由中移除。
// 库里的历史权限行在开发库重建前仍会存在，必须从授权结果中剔除。
func RetiredPermission(code string) bool {
	return strings.HasPrefix(code, "customer_service.") || strings.HasPrefix(code, "ota.") ||
		strings.HasPrefix(code, "alert.rule.") || strings.HasPrefix(code, "alert.subscription.") ||
		code == "settings.ota.update" || code == "membership.create" ||
		code == "alert.risk_config.update" || code == "pricing.template.create"
}

// HasPermission 判断权限清单中是否含指定权限码。
func (p Profile) HasPermission(code string) bool {
	for _, held := range p.Permissions {
		if held == code {
			return true
		}
	}
	return false
}
