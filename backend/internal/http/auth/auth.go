// Package auth 校验用户及后台会话，并将认证错误转换为 HTTP 响应。
package auth

import (
	"errors"
	"net/http"
	"strings"

	identity "chargepilot/internal/identity"
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// NewAuthenticator 组合 JWT、会话和用户状态校验接口，创建 C 端认证器。
// 资源生命周期由 cmd 管理，本包不持有数据库或 Redis 句柄，不改变身份核对顺序。
func NewAuthenticator(jwt *auth.JWT, sessions identity.SessionChecker, users identity.UserStatusChecker) identity.Authenticator {
	return identity.Authenticator{Tokens: identity.JWTAccess{JWT: jwt}, Sessions: sessions, Users: users}
}

// WriteError 将认证错误转为 HTTP 响应：令牌或会话失效返回 401，账号冻结返回 403，其他故障返回 503。
// err=nil 时不写响应；错误响应不暴露内部存储原因。
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

// Authenticate 提取 Bearer 令牌并校验用户会话和账号状态，返回用户编号及是否成功。
// 失败时已写好错误响应，调用方必须立即返回。
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
