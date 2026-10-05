package identity

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// API 只适配用户登录、刷新、注销和档案端点，流程归 identity.SessionService。
type API struct {
	Service identity.SessionService /* cmd 注入的完整用户会话用例 */
}

// Register 在 router 注册原公开身份端点，不启动后台任务或打开资源。
func (a API) Register(router *gin.Engine) {
	router.POST("/api/v1/user/auth/login", a.login)
	router.POST("/api/v1/user/auth/refresh", a.refresh)
	router.POST("/api/v1/user/auth/logout", a.logout)
	router.GET("/api/v1/user/profile", a.profile)
}

// login 从 c 绑定 code 并调用完整身份用例，返回原字段或原阶段 HTTP 错误。
func (a API) login(c *gin.Context) {
	var request struct {
		Code string `json:"code" binding:"required,min=1,max=128"` /* 原交换凭据 */
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

// refresh 从 c 提取原刷新凭据，返回轮换后的访问回执或原错误，流程不在 HTTP 拼接。
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

// logout 从 c 提取刷新凭据并撤销原会话，返回原注销标志或格式／存储错误。
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

// profile 在 c 的取消范围读取认证用户档案，保持原访问／会话／状态错误文案。
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

// bearer 从 c 原头提取单个 Bearer 凭据，格式错误返回空串；不验证或记录明文。
func bearer(c *gin.Context) string {
	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}
