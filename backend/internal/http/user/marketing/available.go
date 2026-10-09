package marketing

import (
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// list 核对 c 原用户凭据并调用所属查询，输出原精简券数组或读取错误，无写入。
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
