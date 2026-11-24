package auth

import (
	"strings"

	"github.com/gin-gonic/gin"
	identity "wattharbor/internal/identity"
)

// SessionAuthenticator 是 C 端鉴权的 HTTP 适配器。
//
// 从请求头提取令牌并把 identity 认证规则的领域错误映射为 HTTP 响应。
type SessionAuthenticator struct {
	Auth identity.Authenticator // cmd 注入的令牌、当前会话及用户状态校验能力，不持有资源句柄
}

// Authenticate 在每个 C 端请求上校验令牌、会话与账号状态。
// ok 为 false 时响应已写好，调用方必须直接返回。
func (a SessionAuthenticator) Authenticate(c *gin.Context) (userID uint64, ok bool) {
	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		WriteError(c, identity.ErrTokenInvalid)
		return 0, false
	}
	return Authenticate(c, a.Auth)
}
