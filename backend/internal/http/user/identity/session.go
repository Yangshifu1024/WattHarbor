// Package identity 提供用户登录、刷新、注销、资料和手机号接口。
package identity

import (
	"errors"
	"net/http"
	"strings"

	"chargepilot/internal/identity"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// API 提供登录、令牌刷新、注销和用户资料接口。
type API struct {
	Service identity.SessionService /* cmd 注入的完整用户会话用例 */
}

// Register 注册用户身份接口，登录和刷新不要求已有访问令牌。
func (a API) Register(router *gin.Engine) {
	router.POST("/api/v1/user/auth/login", a.login)
	router.POST("/api/v1/user/auth/refresh", a.refresh)
	router.POST("/api/v1/user/auth/logout", a.logout)
	router.GET("/api/v1/user/profile", a.profile)
}

// login 用微信登录 code 创建或恢复用户会话。
func (a API) login(c *gin.Context) {
	var request struct {
		Code string `json:"code" binding:"required,min=1,max=128"` /* 交换凭据 */
	}
	if c.ShouldBindJSON(&request) != nil {
		httpapi.BadRequest(c, "invalid login code")
		return
	}
	out, err := a.Service.Login(c.Request.Context(), request.Code)
	if err == nil {
		httpapi.OK(c, out)
		return
	}
	var failure *identity.SessionError
	switch {
	case errors.Is(err, identity.ErrUserFrozen):
		httpapi.Write(c, 403, 1003, "account frozen", nil)
	case errors.As(err, &failure) && failure.Stage == "exchange":
		httpapi.Write(c, http.StatusBadGateway, 3001, "WeChat login failed", nil)
	case failure != nil && failure.Stage == "session":
		httpapi.Write(c, 503, 5001, "session storage unavailable", nil)
	case failure != nil && failure.Stage == "sign":
		httpapi.Write(c, 500, 5001, "token signing failed", nil)
	default:
		httpapi.Write(c, 503, 5001, "login storage unavailable", nil)
	}
}

// refresh 校验刷新令牌，返回新访问令牌和刷新令牌。
func (a API) refresh(c *gin.Context) {
	out, err := a.Service.Refresh(c.Request.Context(), bearer(c))
	if err == nil {
		httpapi.OK(c, out)
		return
	}
	var failure *identity.SessionError
	switch {
	case errors.Is(err, identity.ErrInvalidRefresh):
		httpapi.Write(c, 401, 1001, "refresh token invalid", nil)
	case errors.Is(err, identity.ErrAccountFrozen):
		httpapi.Write(c, 403, 1003, "account frozen", nil)
	case errors.As(err, &failure) && failure.Stage == "sign":
		httpapi.Write(c, 500, 5001, "token signing failed", nil)
	case failure != nil && failure.Stage == "user":
		httpapi.Write(c, 503, 5001, "user status unavailable", nil)
	default:
		httpapi.Write(c, 503, 5001, "session storage unavailable", nil)
	}
}

// logout 校验刷新令牌并撤销对应会话。
func (a API) logout(c *gin.Context) {
	err := a.Service.Logout(c.Request.Context(), bearer(c))
	if errors.Is(err, identity.ErrInvalidRefresh) {
		httpapi.Write(c, 401, 1001, "refresh token invalid", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5001, "session storage unavailable", nil)
		return
	}
	httpapi.OK(c, gin.H{"logged_out": true})
}

// profile 核对当前会话和账号状态，返回用户资料。
func (a API) profile(c *gin.Context) {
	out, err := a.Service.Profile(c.Request.Context(), bearer(c))
	if err == nil {
		httpapi.OK(c, out)
		return
	}
	var failure *identity.SessionError
	switch {
	case errors.Is(err, identity.ErrTokenInvalid):
		httpapi.Write(c, 401, 1001, "access token invalid", nil)
	case errors.Is(err, identity.ErrSessionExpired):
		httpapi.Write(c, 401, 1001, "session expired", nil)
	case errors.Is(err, identity.ErrAccountFrozen):
		httpapi.Write(c, 403, 1003, "account frozen", nil)
	case errors.As(err, &failure) && failure.Stage == "profile":
		httpapi.Write(c, 503, 5001, "profile storage unavailable", nil)
	case failure != nil && failure.Stage == "profile_session":
		httpapi.Write(c, 503, 5001, "session storage unavailable", nil)
	default:
		httpapi.Write(c, 503, 5001, "user status unavailable", nil)
	}
}

// bearer 提取单个 Bearer 令牌；格式错误返回空串。
func bearer(c *gin.Context) string {
	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}
