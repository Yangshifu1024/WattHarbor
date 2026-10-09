package ops

import (
	"context"
	"errors"
	"strconv"
	"time"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/platform/auth"
)

// AccessTTL 保留后台访问令牌八小时寿命，刷新会话仍按原七天绝对期限到期。
const AccessTTL = 8 * time.Hour

var (
	ErrLoginRate  = errors.New("登录过于频繁，请稍后重试") // 同一来源一分钟超过五次认证调用
	ErrPermission = errors.New("没有操作权限")       // 当前账号未持有请求的权限
)

// AuthenticationAccounts 提供所属账号事务与实时档案，不暴露SQL或敏感字段到HTTP。
type AuthenticationAccounts interface {
	// Login 在ctx下验证username/password并记录ip，返回账号或凭据／锁定／存储错误。
	Login(ctx context.Context, username, password, ip string) (AdminAccount, error)
	// CompleteMFA 锁定identity账号并核对原版本、code及ip；成功前调用consume，错误不签发会话。
	CompleteMFA(ctx context.Context, identity operations.AccountIdentity, code, ip string, consume func(context.Context) error) (AdminAccount, error)
	// Profile 在ctx下读取id的当前有效档案及权限，返回凭据／存储错误，不写入。
	Profile(ctx context.Context, id uint64) (operations.Profile, error)
	// ChangePassword 校验id旧口令后更新newPassword和权限版本，ip用于同事务审计；错误回滚。
	ChangePassword(ctx context.Context, id uint64, oldPassword, newPassword, ip string) error
}

// SessionRepository 是后台会话和原登录挑战的所属原子能力，不提供Redis客户端。
type SessionRepository interface {
	// Create 为ctx中的account新建会话，返回SID、刷新明文或随机／存储错误。
	Create(ctx context.Context, account operations.AccountIdentity) (string, string, error)
	// Rotate 竞争token原摘要，返回原身份、SID、新明文或凭据／存储错误，不延寿。
	Rotate(ctx context.Context, token string) (operations.AccountIdentity, string, string, error)
	// Revoke 根据ctx下token当前或旧摘要删除会话，返回凭据／存储错误。
	Revoke(ctx context.Context, token string) error
	// RevokeSession 删除ctx下sid会话，返回空身份／存储错误。
	RevokeSession(ctx context.Context, sid string) error
	// Exists 在ctx下读取sid是否存活，返回事实或Redis错误；不写入。
	Exists(ctx context.Context, sid string) (bool, error)
	// Matches 在ctx下核对sid原id／version，返回事实或读取错误，只读。
	Matches(ctx context.Context, sid string, id, version uint64) (bool, error)
	// BeginMFA 为ctx中的account保存短期挑战，返回随机挑战或存储错误。
	BeginMFA(ctx context.Context, account operations.AccountIdentity) (string, error)
	// ResolveMFA 在ctx下读取token的原挑战身份及CAS凭据，返回缺失／格式／存储错误。
	ResolveMFA(ctx context.Context, token string) (MFAChallenge, error)
	// ConsumeMFA 在ctx下原子消费challenge原凭据，返回已消费／存储错误，拒绝替换后的记录。
	ConsumeMFA(ctx context.Context, challenge MFAChallenge) error
}

// LoginLimiter 只提供计数事实，一分钟五次的准入规则由SessionService决定。
type LoginLimiter interface {
	// CountLogin 在ctx下增加ip来源计数并保留原一分钟窗口，返回计数或存储错误。
	CountLogin(ctx context.Context, ip string) (int, error)
}

// AccessTokens 只签发／校验后台访问令牌，角色权限仍从实时档案读取。
type AccessTokens interface {
	// Sign 用profile身份、sid和now签发八小时令牌，返回签名或错误，无存储。
	Sign(profile operations.Profile, sid string, now time.Time) (string, error)
	// Verify 校验token后台类型及当前时间，返回id、sid或凭据错误，无存储。
	Verify(token string) (uint64, string, error)
}

// JWTAccess 适配进程拥有的JWT，认证业务不直接操作通用声明。
type JWTAccess struct {
	JWT *auth.JWT /* cmd注入的签发校验器 */
}

// Sign 按profile、sid及now生成原admin声明，返回签名或配置／声明错误，不写会话。
func (a JWTAccess) Sign(profile operations.Profile, sid string, now time.Time) (string, error) {
	if a.JWT == nil {
		return "", auth.ErrInvalidToken
	}
	return a.JWT.Sign(auth.Claims{Subject: strconv.FormatUint(profile.ID, 10), Kind: "admin", SessionID: sid, RoleIDs: []uint64{profile.RoleID}, IssuedAt: now.Unix(), ExpiresAt: now.Add(AccessTTL).Unix()})
}

// Verify 校验token的原后台类型与时间，返回原ID／SID或凭据错误；不信任声明内角色授权。
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

// AccessReply 保留后台原公开字段；Profile不包含密码、密钥及权限版本。
type AccessReply struct {
	operations.Profile        // 当前账号、显示名、角色与实时权限
	Token              string `json:"token"`         // 原访问令牌兼容字段
	AccessToken        string `json:"access_token"`  // 同一访问签名
	RefreshToken       string `json:"refresh_token"` // 原刷新明文，仅返回客户端
	ExpiresIn          int64  `json:"expires_in"`    // 访问寿命，秒
}

// LoginOutcome 区分待二次验证和已签发的结果；HTTP按原两种响应形状输出。
type LoginOutcome struct {
	Challenge string      // 仅MFA账号返回的五分钟挑战
	Access    AccessReply // 非MFA账号的完整回执
}

// SessionService 拥有认证、实时权限、轮换、挑战消费和签发失败清理的完整顺序。
type SessionService struct {
	Accounts AuthenticationAccounts // 所属账号及权限事务
	Sessions SessionRepository      // 所属会话与挑战原子能力
	Tokens   AccessTokens           // 后台专属签发校验能力
	Limits   LoginLimiter           // 来源计数能力
}

// CheckRate 在ctx下计数ip认证调用；超过五次返回ErrLoginRate，存储故障原样失败关闭。
// HTTP在解码前调用以保持原坏输入也计数的行为。
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

// Login 在ctx下验证username/password和ip，返回原挑战或会话；失败不返回凭据材料。
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

// VerifyMFA 在ctx下恢复challenge原身份，锁后核对版本／锁定与code；ip进入认证审计。
// 好码后在同一账号事务内CAS消费挑战，只有赢家可写成功审计并发会话；坏码允许原挑战重试。
// Redis消费不与SQL原子提交，消费后SQL失败只回滚SQL且不发会话，调用方须重新密码登录。
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

// completeLogin 在ctx下重读account当前档案，再创建与当前版本一致的会话并签发；错误不返回令牌。
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

// Refresh 在ctx下轮换token后重读原账号档案／版本，再签发；凭据或读取失败尽力撤销新摘要。
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

// issue 在ctx下为profile/sid签发并返回refresh及八小时寿命；签发失败尽力撤销同一会话。
// 清理使用原请求context且可能失败，不把签发失败报告为已完成注销。
func (s SessionService) issue(ctx context.Context, profile operations.Profile, sid, refresh string) (AccessReply, error) {
	token, err := s.Tokens.Sign(profile, sid, time.Now())
	if err != nil {
		_ = s.Sessions.Revoke(ctx, refresh)
		return AccessReply{}, err
	}
	return AccessReply{Profile: profile, Token: token, AccessToken: token, RefreshToken: refresh, ExpiresIn: int64(AccessTTL / time.Second)}, nil
}

// Authenticate 在ctx下核对token类型、当前档案、sid原身份／版本和permission，返回档案／SID或错误。
// JWT中的角色和权限不用于准入；停用、改密、角色版本及权限删除下一请求即时生效。
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

// Logout 在ctx下撤销已鉴权sid，返回空身份／存储错误，完成后该会话的所有访问签名失效。
func (s SessionService) Logout(ctx context.Context, sid string) error {
	return s.Sessions.RevokeSession(ctx, sid)
}

// ChangePassword 在ctx下为当前id核对oldPassword并更新newPassword，ip用于同事务审计。
// 长度／不同口令规则和权限版本变更由所属账号事务执行，返回凭据或存储错误。
func (s SessionService) ChangePassword(ctx context.Context, id uint64, oldPassword, newPassword, ip string) error {
	return s.Accounts.ChangePassword(ctx, id, oldPassword, newPassword, ip)
}
