package identity

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"strconv"
	"time"
)

// AccessTTL 是用户访问令牌的既有15分钟寿命，不改变刷新会话的绝对过期时间。
const AccessTTL = 15 * time.Minute

// UserRepository 提供登录身份、实时启用状态和公开档案，保留稳定接口名。
type UserRepository interface {
	// Login 在 ctx 下恢复或创建 openID/unionID 用户，返回身份或冻结／存储错误。
	Login(ctx context.Context, openID, unionID string) (User, error)
	// Active 在 ctx 下读取 id 当前启用状态，返回事实或读取错误。
	Active(ctx context.Context, id uint64) (bool, error)
	// Profile 在 ctx 下读取 id 档案，返回公开投影或缺失／读取错误。
	Profile(ctx context.Context, id uint64) (Profile, error)
}

// SessionRepository 提供原会话创建、原子轮换、注销和存在性，不包含 HTTP 凭据提取。
type SessionRepository interface {
	// Create 在 ctx 下为 user 创建会话，返回 SID、明文刷新令牌或错误。
	Create(ctx context.Context, user User) (string, string, error)
	// Rotate 在 ctx 下校验 token 并原子轮换，返回身份、原 SID、新刷新令牌或错误。
	Rotate(ctx context.Context, token string) (User, string, string, error)
	// Revoke 在 ctx 下按 token 当前或原摘要撤销会话；格式、归属或存储失败返回错误。
	Revoke(ctx context.Context, token string) error
	// Exists 在 ctx 下读取 sid 是否存活，返回事实或存储错误。
	Exists(ctx context.Context, sid string) (bool, error)
}

// AccessTokens 限定为用户访问令牌签发和校验；实现须拒绝其他来源的令牌。
type AccessTokens interface {
	TokenVerifier // 校验并返回原用户、会话身份
	// Sign 为 user/sid 按 now 签发15分钟令牌，返回签名或错误；不写会话。
	Sign(user User, sid string, now time.Time) (string, error)
}

// JWTAccess 适配进程拥有的 JWT 能力；identity 的消费者只依赖 AccessTokens。
type JWTAccess struct {
	JWT *auth.JWT /* 进程注入的签名与校验器 */
}

// Sign 为 user、sid 按 now 签发原 user 类型令牌，返回签名或声明／密钥错误；无持久写入。
func (a JWTAccess) Sign(user User, sid string, now time.Time) (string, error) {
	if a.JWT == nil {
		return "", auth.ErrInvalidToken
	}
	return a.JWT.Sign(auth.Claims{Subject: strconv.FormatUint(user.ID, 10), Kind: "user", OpenID: user.OpenID, SessionID: sid, IssuedAt: now.Unix(), ExpiresAt: now.Add(AccessTTL).Unix()})
}

// Verify 校验 token 的原 user 类型和当前时间，返回 subject、sid 或签名／声明错误；无 IO。
func (a JWTAccess) Verify(token string) (string, string, error) {
	if a.JWT == nil {
		return "", "", auth.ErrInvalidToken
	}
	claims, err := a.JWT.Verify(token, "user", time.Now())
	if err != nil {
		return "", "", err
	}
	return claims.Subject, claims.SessionID, nil
}

// SessionError 保留登录／刷新失败阶段，公开 HTTP 仅映射原状态和文案。
type SessionError struct {
	Stage string /* exchange/user/session/sign/refresh/revoke/profile */
	Cause error  /* 原错误，不包含返回给客户端的令牌 */
}

// Error 返回原原因或阶段文本；无 IO。
func (e *SessionError) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.Stage
}

// Unwrap 返回原原因以供领域哨兵匹配；无副作用。
func (e *SessionError) Unwrap() error { return e.Cause }

// AccessResult 保留原访问及刷新响应字段，访问令牌兼容 jwt/token 两个键。
type AccessResult struct {
	JWT          string `json:"jwt"`            // 原访问签名
	Token        string `json:"token"`          // 同一访问签名的兼容键
	RefreshToken string `json:"refresh_token"`  // 新签发或轮换的原刷新明文
	JWTExpiresIn int    `json:"jwt_expires_in"` // 访问寿命，秒
}

// LoginResult 增加登录独有的十进制身份及新用户标志，不改变刷新响应字段。
type LoginResult struct {
	AccessResult        // 原访问／刷新回执
	UserID       string `json:"user_id"`     // 原用户身份，避免数值精度丢失
	IsNewUser    bool   `json:"is_new_user"` // 本次是否首次创建
}

// SessionService 拥有交换、用户准入、会话轮换、签发及失败清理的顺序。
type SessionService struct {
	WeChat   CodeExchanger     // cmd 注入正式或开发交换能力
	Users    UserRepository    // 所属用户准入与档案能力
	Sessions SessionRepository // 所属原子会话实现
	Tokens   AccessTokens      // 用户类型的签发和校验能力
}

// Login 在 ctx 下交换 code、恢复用户、创建会话并签发，返回原回执或阶段错误。
// 用户冻结不创建会话；签发失败尽力撤销刚创建的刷新会话，沿用原失败清理契约。
func (s SessionService) Login(ctx context.Context, code string) (LoginResult, error) {
	identity, err := s.WeChat.Exchange(ctx, code)
	if err != nil {
		return LoginResult{}, &SessionError{"exchange", err}
	}
	user, err := s.Users.Login(ctx, identity.OpenID, identity.UnionID)
	if err != nil {
		return LoginResult{}, &SessionError{"user", err}
	}
	sid, refresh, err := s.Sessions.Create(ctx, user)
	if err != nil {
		return LoginResult{}, &SessionError{"session", err}
	}
	access, err := s.Tokens.Sign(user, sid, time.Now())
	if err != nil {
		_ = s.Sessions.Revoke(ctx, refresh)
		return LoginResult{}, &SessionError{"sign", err}
	}
	return LoginResult{AccessResult{access, access, refresh, int(AccessTTL.Seconds())}, strconv.FormatUint(user.ID, 10), user.IsNew}, nil
}

// Refresh 在 ctx 下原子轮换 token 后核对用户状态并签发，返回刷新字段或阶段错误。
// 不延长 Redis 寿命；冻结或签发失败尽力撤销新令牌对应会话，查询失败沿用原存储错误。
func (s SessionService) Refresh(ctx context.Context, token string) (AccessResult, error) {
	user, sid, refresh, err := s.Sessions.Rotate(ctx, token)
	if err != nil {
		return AccessResult{}, &SessionError{"refresh", err}
	}
	active, err := s.Users.Active(ctx, user.ID)
	if err != nil {
		return AccessResult{}, &SessionError{"user", err}
	}
	if !active {
		_ = s.Sessions.Revoke(ctx, refresh)
		return AccessResult{}, ErrAccountFrozen
	}
	access, err := s.Tokens.Sign(user, sid, time.Now())
	if err != nil {
		_ = s.Sessions.Revoke(ctx, refresh)
		return AccessResult{}, &SessionError{"sign", err}
	}
	return AccessResult{access, access, refresh, int(AccessTTL.Seconds())}, nil
}

// Logout 在 ctx 下按 token 当前或已轮换摘要撤销原会话，返回格式／归属／存储错误。
func (s SessionService) Logout(ctx context.Context, token string) error {
	if err := s.Sessions.Revoke(ctx, token); err != nil {
		return &SessionError{"revoke", err}
	}
	return nil
}

// Authenticate 在 ctx 下核对 token、原会话及实时用户状态，返回用户或原身份哨兵；不写入。
func (s SessionService) Authenticate(ctx context.Context, token string) (uint64, error) {
	return (Authenticator{Tokens: s.Tokens, Sessions: s.Sessions, Users: s.Users}).Authenticate(ctx, token)
}

// Profile 在 ctx 下认证 token 后读取所属公开档案，返回投影或身份／档案错误，不返回他人身份。
func (s SessionService) Profile(ctx context.Context, token string) (Profile, error) {
	if s.Tokens == nil {
		return Profile{}, ErrTokenInvalid
	}
	subject, sid, err := s.Tokens.Verify(token)
	if err != nil || sid == "" {
		return Profile{}, ErrTokenInvalid
	}
	exists, err := s.Sessions.Exists(ctx, sid)
	if err != nil {
		return Profile{}, &SessionError{"profile_session", err}
	}
	if !exists {
		return Profile{}, ErrSessionExpired
	}
	id, err := strconv.ParseUint(subject, 10, 64)
	if err != nil || id == 0 {
		return Profile{}, ErrTokenInvalid
	}
	active, err := s.Users.Active(ctx, id)
	if err != nil {
		return Profile{}, &SessionError{"profile_user", err}
	}
	if !active {
		return Profile{}, ErrAccountFrozen
	}
	profile, err := s.Users.Profile(ctx, id)
	if err != nil {
		return Profile{}, &SessionError{"profile", err}
	}
	return profile, nil
}
