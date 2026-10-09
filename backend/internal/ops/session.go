package ops

import (
	"context"
	"errors"
	"strconv"
	"time"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/platform/auth"
)

// AccessTTL 是八小时访问令牌有效期，刷新会话保持七天绝对期限。
const AccessTTL = 8 * time.Hour

var (
	ErrLoginRate  = errors.New("登录过于频繁，请稍后重试") // 同一来源一分钟超过五次认证调用
	ErrPermission = errors.New("没有操作权限")       // 当前账号未持有请求的权限
)

// AuthenticationAccounts 提供账号事务和当前资料，认证材料不暴露到 HTTP。
type AuthenticationAccounts interface {
	// Login 验证用户名和密码，IP 用于登录审计。
	Login(ctx context.Context, username, password, ip string) (AdminAccount, error)
	// CompleteMFA 锁定账号后核对版本和验证码，成功前删除挑战；失败不签发会话。
	CompleteMFA(ctx context.Context, identity operations.AccountIdentity, code, ip string, consume func(context.Context) error) (AdminAccount, error)
	// Profile 读取当前有效账号和权限。
	Profile(ctx context.Context, id uint64) (operations.Profile, error)
	// ChangePassword 校验旧密码后更新新密码和认证版本，IP 用于同事务审计。
	ChangePassword(ctx context.Context, id uint64, oldPassword, newPassword, ip string) error
}

// SessionRepository 管理后台会话和登录挑战，不暴露 Redis 客户端。
type SessionRepository interface {
	// Create 新建会话，返回 SID 和刷新令牌明文。
	Create(ctx context.Context, account operations.AccountIdentity) (string, string, error)
	// Rotate 校验刷新令牌摘要后返回账号、SID 和新令牌，不延长首次到期时间。
	Rotate(ctx context.Context, token string) (operations.AccountIdentity, string, string, error)
	// Revoke 核对当前或旧刷新摘要后删除会话。
	Revoke(ctx context.Context, token string) error
	// RevokeSession 删除指定 SID 的会话。
	RevokeSession(ctx context.Context, sid string) error
	// Exists 检查 SID 会话是否存活。
	Exists(ctx context.Context, sid string) (bool, error)
	// Matches 检查 SID 对应的账号 ID 和认证版本。
	Matches(ctx context.Context, sid string, id, version uint64) (bool, error)
	// BeginMFA 为账号保存短期挑战并返回挑战码。
	BeginMFA(ctx context.Context, account operations.AccountIdentity) (string, error)
	// ResolveMFA 读取挑战中的账号资料和读取时的完整文本。
	ResolveMFA(ctx context.Context, token string) (MFAChallenge, error)
	// ConsumeMFA 比较读取的挑战并原子删除，拒绝已替换记录。
	ConsumeMFA(ctx context.Context, challenge MFAChallenge) error
}

// LoginLimiter 仅统计次数；每分钟五次的限制由 SessionService 判断。
type LoginLimiter interface {
	// CountLogin 统计 IP 登录次数，保持首次一分钟窗口。
	CountLogin(ctx context.Context, ip string) (int, error)
}

// AccessTokens 只签发／校验后台访问令牌，角色权限仍从实时档案读取。
type AccessTokens interface {
	// Sign 根据账号、SID 和当前时间签发八小时访问令牌。
	Sign(profile operations.Profile, sid string, now time.Time) (string, error)
	// Verify 校验后台令牌类型和时间，返回账号 ID 和 SID。
	Verify(token string) (uint64, string, error)
}

// JWTAccess 适配进程拥有的 JWT，认证业务不直接操作通用声明。
type JWTAccess struct {
	JWT *auth.JWT /* cmd 注入的签发校验器 */
}

// Sign 根据账号、SID 和当前时间签发后台令牌，不写会话。
func (a JWTAccess) Sign(profile operations.Profile, sid string, now time.Time) (string, error) {
	if a.JWT == nil {
		return "", auth.ErrInvalidToken
	}
	return a.JWT.Sign(auth.Claims{Subject: strconv.FormatUint(profile.ID, 10), Kind: "admin", SessionID: sid, RoleIDs: []uint64{profile.RoleID}, IssuedAt: now.Unix(), ExpiresAt: now.Add(AccessTTL).Unix()})
}

// Verify 校验后台类型和有效期，返回 ID 和 SID；令牌内的角色不能作为授权依据。
func (a JWTAccess) Verify(token string) (uint64, string, error) {
	if a.JWT == nil {
		return 0, "", auth.ErrInvalidToken
	}
	claims, err := a.JWT.Verify(token, "admin", time.Now())
	if err != nil {
		return 0, "", err
	}
	id, err := strconv.ParseUint(claims.Subject, 10, 64)
	if err != nil || id == 0 || claims.SessionID == "" {
		return 0, "", auth.ErrInvalidToken
	}
	return id, claims.SessionID, nil
}

// AccessReply 是后台令牌响应，Profile 排除密码、密钥和认证版本。
type AccessReply struct {
	operations.Profile        // 当前账号、显示名、角色与实时权限
	Token              string `json:"token"`         // 访问令牌兼容字段
	AccessToken        string `json:"access_token"`  // 同一访问签名
	RefreshToken       string `json:"refresh_token"` // 刷新明文，仅返回客户端
	ExpiresIn          int64  `json:"expires_in"`    // 访问寿命，秒
}

// LoginOutcome 区分待 MFA 和已签发会话，HTTP 返回对应结果。
type LoginOutcome struct {
	Challenge string      // 仅 MFA 账号返回的五分钟挑战
	Access    AccessReply // 非 MFA 账号的完整回执
}

// SessionService 负责登录、当前权限校验、令牌轮换、挑战删除和签发失败清理。
type SessionService struct {
	Accounts AuthenticationAccounts // 所属账号及权限事务
	Sessions SessionRepository      // 后台会话和挑战管理接口
	Tokens   AccessTokens           // 后台访问令牌签发和校验接口
	Limits   LoginLimiter           // 按 IP 统计认证调用次数的接口
}

// CheckRate 统计 IP 认证次数，超过每分钟五次返回 ErrLoginRate；存储失败拒绝登录。
// HTTP 解码前调用，非法输入也计数。
func (s SessionService) CheckRate(ctx context.Context, ip string) error {
	if s.Limits == nil {
		return errors.New("login limiter unavailable")
	}
	n, err := s.Limits.CountLogin(ctx, ip)
	if err != nil {
		return err
	}
	if n > 5 {
		return ErrLoginRate
	}
	return nil
}

// Login 校验用户名和密码，IP 用于审计；成功返回 MFA 挑战或令牌，失败不返回密码或密钥。
func (s SessionService) Login(ctx context.Context, username, password, ip string) (LoginOutcome, error) {
	account, err := s.Accounts.Login(ctx, username, password, ip)
	if err != nil {
		return LoginOutcome{}, err
	}
	if account.MFAEnabled {
		challenge, err := s.Sessions.BeginMFA(ctx, account.Identity())
		return LoginOutcome{Challenge: challenge}, err
	}
	access, err := s.completeLogin(ctx, account)
	return LoginOutcome{Access: access}, err
}

// VerifyMFA 锁定挑战账号后核对版本、锁定状态和验证码。
// 验证码正确时，在账号事务内比较并删除挑战；只有成功删除者能登录并创建会话，错误验证码可重试。
// Redis 删除不与 SQL 原子提交，SQL 失败须重新密码登录。
func (s SessionService) VerifyMFA(ctx context.Context, token, code, ip string) (AccessReply, error) {
	challenge, err := s.Sessions.ResolveMFA(ctx, token)
	if err != nil {
		return AccessReply{}, err
	}
	account, err := s.Accounts.CompleteMFA(ctx, challenge.Account, code, ip, func(ctx context.Context) error {
		return s.Sessions.ConsumeMFA(ctx, challenge)
	})
	if err != nil {
		return AccessReply{}, err
	}
	return s.completeLogin(ctx, account)
}

// completeLogin 读取当前账号资料，创建与认证版本一致的会话并签发令牌。
func (s SessionService) completeLogin(ctx context.Context, account AdminAccount) (AccessReply, error) {
	profile, err := s.Accounts.Profile(ctx, account.ID)
	if err != nil {
		return AccessReply{}, err
	}
	if profile.AuthVersion != account.AuthVersion {
		return AccessReply{}, operations.ErrCredentials
	}
	sid, refresh, err := s.Sessions.Create(ctx, account.Identity())
	if err != nil {
		return AccessReply{}, err
	}
	return s.issue(ctx, profile, sid, refresh)
}

// Refresh 轮换刷新令牌后再核对账号和版本并签发，失败时尝试撤销新摘要。
func (s SessionService) Refresh(ctx context.Context, token string) (AccessReply, error) {
	account, sid, refresh, err := s.Sessions.Rotate(ctx, token)
	if err != nil {
		return AccessReply{}, err
	}
	profile, err := s.Accounts.Profile(ctx, account.ID)
	if err == nil && account.AuthVersion != profile.AuthVersion {
		err = operations.ErrCredentials
	}
	if err != nil {
		_ = s.Sessions.Revoke(ctx, refresh)
		return AccessReply{}, err
	}
	return s.issue(ctx, profile, sid, refresh)
}

// issue 签发八小时访问令牌，并返回已生成的刷新令牌；签发失败时尝试撤销会话。
// 清理使用首次请求 context，失败时不能声称已注销。
func (s SessionService) issue(ctx context.Context, profile operations.Profile, sid, refresh string) (AccessReply, error) {
	token, err := s.Tokens.Sign(profile, sid, time.Now())
	if err != nil {
		_ = s.Sessions.Revoke(ctx, refresh)
		return AccessReply{}, err
	}
	return AccessReply{Profile: profile, Token: token, AccessToken: token, RefreshToken: refresh, ExpiresIn: int64(AccessTTL / time.Second)}, nil
}

// Authenticate 核对令牌类型、最新账号权限及会话账号和版本。
// 不使用 JWT 中的旧角色或权限；停用、改密、角色版本和权限删除在下一请求生效。
func (s SessionService) Authenticate(ctx context.Context, token, permission string) (operations.Profile, string, error) {
	if s.Tokens == nil || s.Accounts == nil || s.Sessions == nil {
		return operations.Profile{}, "", operations.ErrCredentials
	}
	id, sid, err := s.Tokens.Verify(token)
	if err != nil {
		return operations.Profile{}, "", operations.ErrCredentials
	}
	exists, err := s.Sessions.Exists(ctx, sid)
	if err != nil {
		return operations.Profile{}, "", err
	}
	if !exists {
		return operations.Profile{}, "", operations.ErrCredentials
	}
	profile, err := s.Accounts.Profile(ctx, id)
	if err != nil {
		return operations.Profile{}, "", err
	}
	match, err := s.Sessions.Matches(ctx, sid, profile.ID, profile.AuthVersion)
	if err != nil {
		return operations.Profile{}, "", err
	}
	if !match {
		return operations.Profile{}, "", operations.ErrCredentials
	}
	if permission != "" && !profile.HasPermission(permission) {
		return operations.Profile{}, "", ErrPermission
	}
	return profile, sid, nil
}

// Logout 撤销已鉴权会话，使该 SID 的所有访问令牌失效。
func (s SessionService) Logout(ctx context.Context, sid string) error {
	return s.Sessions.RevokeSession(ctx, sid)
}

// ChangePassword 验证旧密码并更新新密码，认证版本与审计在账号事务中保存。
func (s SessionService) ChangePassword(ctx context.Context, id uint64, oldPassword, newPassword, ip string) error {
	return s.Accounts.ChangePassword(ctx, id, oldPassword, newPassword, ip)
}
