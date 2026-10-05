package auth

import (
	"errors"
	"net/http"
	"strings"

	identity "github.com/ChargePilot2026/charge-pilot/internal/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// NewAuthenticator 以jwt、sessions及users装配原C端令牌、会话存在性及当前用户状态校验，无IO或错误。
// 资源生命周期由cmd管理，本包不持有数据库或Redis句柄，不改变身份核对顺序。
func NewAuthenticator(jwt *auth.JWT, sessions identity.SessionChecker, users identity.UserStatusChecker) identity.Authenticator {
	return identity.Authenticator{Tokens: identity.JWTAccess{JWT: jwt}, Sessions: sessions, Users: users}
}

// WriteError 将err领域哨兵写入c原响应；nil不写，凭据／会话401、冻结403、其它503，不公开存储原因。
func WriteError(c *gin.Context, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, identity.ErrTokenInvalid), errors.Is(err, identity.ErrSessionExpired):
		httpapi.Write(c, http.StatusUnauthorized, 1001, "登录已失效，请重新登录", nil)
	case errors.Is(err, identity.ErrAccountFrozen):
		httpapi.Write(c, http.StatusForbidden, 1003, "账号已冻结", nil)
	default:
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "认证服务暂时不可用，请稍后重试", nil)
	}
}

// Authenticate 从c提取原Bearer并以a在请求context下核对，返回用户ID及成功标志。
// 格式、会话、冻结或存储失败均返回0／false且已写响应，调用者立即返回，不继续读取用户数据。
func Authenticate(c *gin.Context, a identity.Authenticator) (uint64, bool) {
	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "登录已失效，请重新登录", nil)
		return 0, false
	}
	id, err := a.Authenticate(c.Request.Context(), parts[1])
	if err != nil {
		WriteError(c, err)
		return 0, false
	}
	return id, true
}
