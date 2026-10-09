package auth

import (
	"net/http"

	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// RequireServiceToken 返回按 token 校验 X-Service-Token 的内部路由中间件。
// 空配置或不匹配写 401 并 Abort，成功才执行后续处理器。
func RequireServiceToken(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !serviceclient.VerifyServiceToken(token, c.GetHeader("X-Service-Token")) {
			httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}
