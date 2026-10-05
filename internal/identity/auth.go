// 本文件定义用户访问令牌校验与实时账号状态判定。
// 不认识 HTTP，失败一律以哨兵错误返回，由接口层翻译成状态码。
package identity

import (
	"context"
	"errors"
	"strconv"
)

// 认证失败的四类原因。区分它们是为了让接口层能各自映射到正确的 HTTP 状态：
// 凭据问题回 401，会话过期回 401，存储故障回 503（可重试），账号冻结回 403。
var (
	ErrTokenInvalid    = errors.New("access token invalid")
	ErrSessionExpired  = errors.New("session expired")
	ErrUserUnavailable = errors.New("user storage unavailable")
	ErrAccountFrozen   = errors.New("account frozen")
)

// SessionChecker 判断会话是否仍然存活。
type SessionChecker interface {
	// Exists 在请求 context 下读取 SID 是否仍存活，返回事实或存储错误，不延长寿命。
	Exists(context.Context, string) (bool, error)
}

// UserStatusChecker 读取账号当前状态，冻结账号不得继续使用会话。
type UserStatusChecker interface {
	// Active 在请求 context 下读取用户主键的当前启用状态，返回事实或读取错误，不改状态。
	Active(context.Context, uint64) (bool, error)
}

// TokenVerifier 校验签名并取出声明。实现方负责选择算法与密钥管理，
// 并自行拒绝 kind 不匹配的令牌——C 端与后台的令牌不通用。
type TokenVerifier interface {
	// Verify 校验 token，返回原 subject、sessionID 或签名／声明错误；不执行持久写入。
	Verify(token string) (subject string, sessionID string, err error)
}

// Authenticator 校验一份访问令牌，返回令牌所属的用户 ID。
//
// 它不认识 HTTP：失败一律以哨兵错误返回，由 http/auth 的适配器翻译成
// 状态码与响应体。这样业务包可以依赖本包而不必依赖 gin。
type Authenticator struct {
	Tokens   TokenVerifier     // 用户类型的访问令牌校验器
	Sessions SessionChecker    // 当前会话存活能力
	Users    UserStatusChecker // 当前账号启用状态能力
}

// Authenticate 依次校验令牌签名、会话存活与账号状态。
//
// ctx 控制会话和状态读取，token 为原访问签名；返回原用户主键或四类哨兵之一，无写入。
func (a Authenticator) Authenticate(ctx context.Context, token string) (uint64, error) {
	if token == "" || a.Tokens == nil {
		return 0, ErrTokenInvalid
	}
	subject, sessionID, err := a.Tokens.Verify(token)
	if err != nil || sessionID == "" {
		return 0, ErrTokenInvalid
	}
	if a.Sessions == nil {
		return 0, ErrUserUnavailable
	}
	valid, err := a.Sessions.Exists(ctx, sessionID)
	if err != nil {
		return 0, ErrUserUnavailable
	}
	if !valid {
		return 0, ErrSessionExpired
	}
	id, err := strconv.ParseUint(subject, 10, 64)
	if err != nil || id == 0 {
		return 0, ErrTokenInvalid
	}
	if a.Users == nil {
		return 0, ErrUserUnavailable
	}
	active, err := a.Users.Active(ctx, id)
	if err != nil {
		return 0, ErrUserUnavailable
	}
	if !active {
		return 0, ErrAccountFrozen
	}
	return id, nil
}
