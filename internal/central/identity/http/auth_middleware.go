package userhttp

import (
	"strings"

	"github.com/gin-gonic/gin"

	identity "github.com/ChargePilot2026/charge-pilot/internal/central/identity/user"
)

// SessionAuthenticator 是 C 端鉴权的 HTTP 适配器。
//
// 认证规则本身在 identity/user 包，本类型只做两件事：从请求头取出令牌、
// 把领域哨兵错误翻译成 HTTP 状态码。判定顺序与错误语义见该包。
type SessionAuthenticator struct {
	Auth identity.Authenticator
}

// Authenticate 在每个 C 端请求上校验令牌、会话与账号状态。
// ok 为 false 时响应已写好，调用方必须直接返回。
func (a SessionAuthenticator) Authenticate(c *gin.Context) (userID uint64, ok bool) {
	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		authError(c, identity.ErrTokenInvalid)
		return 0, false
	}
	return authenticate(c, a.Auth)
}
