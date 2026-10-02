package http

import (
	"errors"
	"net/http"
	"strings"
	"time"

	identityuser "github.com/ChargePilot2026/charge-pilot/internal/domain/identity/user"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// userTokenVerifier 把 platform/auth.JWT 适配成 identityuser.TokenVerifier。
// kind 固定为 user，后台令牌不能用于 C 端接口。
type userTokenVerifier struct{ jwt *auth.JWT }

func (v userTokenVerifier) Verify(token string) (string, string, error) {
	claims, err := v.jwt.Verify(token, "user", time.Now())
	if err != nil {
		return "", "", err
	}
	return claims.Subject, claims.SessionID, nil
}

// NewAuthenticator 装配 C 端鉴权器。sessions 与 users 由调用方注入，
// 本包不持有数据库或 Redis 句柄。
func NewAuthenticator(jwt *auth.JWT, sessions identityuser.SessionChecker, users identityuser.UserStatusChecker) identityuser.Authenticator {
	return identityuser.Authenticator{Tokens: userTokenVerifier{jwt: jwt}, Sessions: sessions, Users: users}
}

// authError 把领域哨兵翻译成 HTTP 响应。四类原因对应四种状态：
// 凭据无效与会话过期回 401，存储故障回 503，账号冻结回 403。
func authError(c *gin.Context, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, identityuser.ErrTokenInvalid), errors.Is(err, identityuser.ErrSessionExpired):
		httpapi.Write(c, http.StatusUnauthorized, 1001, "登录已失效，请重新登录", nil)
	case errors.Is(err, identityuser.ErrAccountFrozen):
		httpapi.Write(c, http.StatusForbidden, 1003, "账号已冻结", nil)
	default:
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "认证服务暂时不可用，请稍后重试", nil)
	}
}

// authenticate 从请求头取令牌并校验，失败时已写好响应。
func authenticate(c *gin.Context, a identityuser.Authenticator) (uint64, bool) {
	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "登录已失效，请重新登录", nil)
		return 0, false
	}
	id, err := a.Authenticate(c.Request.Context(), parts[1])
	if err != nil {
		authError(c, err)
		return 0, false
	}
	return id, true
}
