package admin

import (
	"net/http"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// RequireServiceToken 返回按token校验X-Service-Token的内部路由中间件。
// 空配置或不匹配写401并Abort，成功才执行后续处理器；不执行业务IO。
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
