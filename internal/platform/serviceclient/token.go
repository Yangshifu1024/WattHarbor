package serviceclient

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// serviceTokenHeader 是服务间调用的鉴权头名。前端永远不持有该令牌。
const serviceTokenHeader = "X-Service-Token"

// VerifyServiceToken 校验服务间调用携带的令牌。
//
// 比较前先对两侧取 SHA-256 摘要，使不同长度的输入也能定长比较，
// 再用 ConstantTimeCompare 避免时序侧信道。期望令牌为空时一律拒绝——
// 未配置令牌意味着配置错误，放行等于让接口裸奔。
func VerifyServiceToken(expected, provided string) bool {
	if expected == "" {
		return false
	}
	want := sha256.Sum256([]byte(expected))
	got := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}

// RequireServiceToken 是 gin 中间件形态的封装：校验失败即写 401 并中止，
// 调用方直接注册即可，不需要在每个处理器里重复判断。
func RequireServiceToken(expected string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !VerifyServiceToken(expected, c.GetHeader(serviceTokenHeader)) {
			httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}
