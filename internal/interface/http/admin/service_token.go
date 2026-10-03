package admin

import (
	"net/http"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// RequireServiceToken 校验内部调用的服务令牌。令牌未配置时一律拒绝，
// 避免未配置即裸奔。
//
// 这段校验此前在 regulatory、webhook、device-alerts 三处各写一份，
// 其中一处还漏了 Abort。收敛到共享层后行为一致，族内不再各自判断。
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
