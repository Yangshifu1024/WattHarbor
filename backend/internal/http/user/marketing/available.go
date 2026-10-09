package marketing

import (
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// list 返回当前用户可领取的优惠券。
func (a API) list(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	rows, err := a.Query.Available(c.Request.Context(), userID)
	if err != nil {
		httpapi.Write(c, 503, 5003, "优惠券暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}
