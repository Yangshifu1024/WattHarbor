package auth

import (
	"errors"
	"net/http"
	"strings"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// AdminAuth 是后台认证 HTTP 适配；会话、实时权限和安全设置顺序由服务负责。
type AdminAuth struct {
	Sessions ops.SessionService // 后台认证与实时授权
	MFA      ops.MFAService     // 当前账号安全设置
}

// Register 在 r 挂载认证和文档路径；除登录、挑战、刷新外均要求实时登录。
func (a AdminAuth) Register(r *gin.Engine) {
	r.GET("/api/docs/admin.openapi.json", func(c *gin.Context) { c.Data(200, "application/json; charset=utf-8", OpenAPIDocument) })
	r.POST("/api/v1/admin/auth/login", a.login)
	r.POST("/api/v1/admin/auth/mfa", a.verifyMFA)
	r.POST("/api/v1/admin/auth/refresh", a.refresh)
	r.POST("/api/v1/admin/auth/change-password", a.Require(""), a.changePassword)
	r.POST("/api/v1/admin/auth/mfa-settings", a.Require(""), a.mfaSettings)
	r.POST("/api/v1/admin/auth/logout", a.Require(""), a.logout)
	r.GET("/api/v1/admin/auth/me", a.Require(""), func(c *gin.Context) { httpapi.OK(c, c.MustGet("admin_profile")) })
}

// rate 根据请求 IP 检查频率限制；超限时返回 Retry-After 和错误响应。
func (a AdminAuth) rate(c *gin.Context) bool {
	err := a.Sessions.CheckRate(c.Request.Context(), c.ClientIP())
	if err == nil {
		return true
	}
	if errors.Is(err, ops.ErrLoginRate) {
		c.Header("Retry-After", "60")
		httpapi.Write(c, 429, 4291, err.Error(), nil)
	} else {
		a.failure(c, err)
	}
	return false
}

// login 从请求提取账号、口令和 IP，返回服务的挑战或会话回执，失败不输出凭据。
func (a AdminAuth) login(c *gin.Context) {
	if !a.rate(c) {
		return
	}
	var request struct {
		Username string `json:"username" binding:"required,max=64"` // 登录名
		Password string `json:"password" binding:"required,max=72"` // 当前口令
	}
	if c.ShouldBindJSON(&request) != nil {
		httpapi.BadRequest(c, "请输入用户名和密码")
		return
	}
	result, err := a.Sessions.Login(c.Request.Context(), request.Username, request.Password, c.ClientIP())
	if err != nil {
		a.failure(c, err)
		return
	}
	if result.Challenge != "" {
		httpapi.OK(c, gin.H{"mfa_required": true, "mfa_challenge": result.Challenge})
		return
	}
	httpapi.OK(c, result.Access)
}

// verifyMFA 读取登录挑战和验证码，交服务验证后返回会话或错误。
func (a AdminAuth) verifyMFA(c *gin.Context) {
	if !a.rate(c) {
		return
	}
	var request struct {
		Challenge string `json:"mfa_challenge" binding:"required,max=128"` // 密码验证挑战
		Code      string `json:"code" binding:"required,max=16"`           // 当前 TOTP 验证码
	}
	if c.ShouldBindJSON(&request) != nil {
		httpapi.BadRequest(c, "请输入验证码")
		return
	}
	reply, err := a.Sessions.VerifyMFA(c.Request.Context(), request.Challenge, request.Code, c.ClientIP())
	if err != nil {
		a.failure(c, err)
		return
	}
	httpapi.OK(c, reply)
}

// refresh 从请求提取刷新明文，返回轮换回执；坏输入及错误保留响应。
func (a AdminAuth) refresh(c *gin.Context) {
	if !a.rate(c) {
		return
	}
	var request struct {
		RefreshToken string `json:"refresh_token" binding:"required,max=256"` /* 刷新凭据 */
	}
	if c.ShouldBindJSON(&request) != nil {
		httpapi.BadRequest(c, "invalid refresh token")
		return
	}
	reply, err := a.Sessions.Refresh(c.Request.Context(), request.RefreshToken)
	if err != nil {
		a.failure(c, err)
		return
	}
	httpapi.OK(c, reply)
}

// Require 为 permission 返回中间件，从请求提取 Bearer 调用实时认证，成功写入档案与 SID。
func (a AdminAuth) Require(permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if !strings.HasPrefix(raw, "Bearer ") {
			a.failure(c, operations.ErrCredentials)
			c.Abort()
			return
		}
		p, sid, err := a.Sessions.Authenticate(c.Request.Context(), strings.TrimPrefix(raw, "Bearer "), permission)
		if err != nil {
			a.failure(c, err)
			c.Abort()
			return
		}
		c.Set("admin_profile", p)
		c.Set("admin_sid", sid)
		c.Next()
	}
}

// logout 将 c 已鉴权 SID 交给注销，成功返回 revoked 标识，失败映射存储错误。
func (a AdminAuth) logout(c *gin.Context) {
	if err := a.Sessions.Logout(c.Request.Context(), c.GetString("admin_sid")); err != nil {
		a.failure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"revoked": true})
}

// failure 将 c 中的 err 映射为凭据、锁定、验证码、挑战或权限响应，其余 503 不公开内部材料。
func (a AdminAuth) failure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, operations.ErrCredentials), errors.Is(err, operations.ErrInvalidRefresh):
		httpapi.Write(c, http.StatusUnauthorized, 1001, "用户名或密码错误，或登录已失效", nil)
	case errors.Is(err, operations.ErrLocked):
		httpapi.Write(c, 423, 2006, err.Error(), nil)
	case errors.Is(err, operations.ErrUsedTOTP):
		httpapi.Write(c, http.StatusUnauthorized, 1001, operations.ErrUsedTOTP.Error(), nil)
	case errors.Is(err, operations.ErrInvalidTOTP):
		httpapi.Write(c, http.StatusUnauthorized, 1001, "验证码无效或已过期", nil)
	case errors.Is(err, operations.ErrInvalidMFAChallenge):
		httpapi.Write(c, http.StatusUnauthorized, 1001, "登录已失效，请重新登录", nil)
	case errors.Is(err, ops.ErrPermission):
		httpapi.Write(c, 403, 1003, err.Error(), nil)
	default:
		httpapi.Write(c, 503, 5003, "登录服务暂不可用", nil)
	}
}

// changePassword 从请求提取新旧口令并校验公开长度，将账号和 IP 交给改密事务。
func (a AdminAuth) changePassword(c *gin.Context) {
	var request struct {
		OldPassword string `json:"old_password" binding:"required,max=72"`        // 当前口令
		NewPassword string `json:"new_password" binding:"required,min=12,max=72"` // 不同的新口令
	}
	if c.ShouldBindJSON(&request) != nil || len(request.NewPassword) < 12 || len(request.NewPassword) > 72 || request.OldPassword == request.NewPassword {
		httpapi.BadRequest(c, "新密码须与旧密码不同且长度为 12–72 字节")
		return
	}
	p := c.MustGet("admin_profile").(operations.Profile)
	if err := a.Sessions.ChangePassword(c.Request.Context(), p.ID, request.OldPassword, request.NewPassword, c.ClientIP()); err != nil {
		a.failure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"changed": true, "sessions_revoked": true})
}
