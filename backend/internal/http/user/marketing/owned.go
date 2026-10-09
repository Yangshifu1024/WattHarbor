package marketing

import (
	"errors"

	"chargepilot/internal/marketing"
	"chargepilot/internal/persist"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// myCoupons 按分页和状态返回当前用户持有的优惠券。
func (a API) myCoupons(c *gin.Context) {
	id, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	page, size, ok := httpapi.ReadPaging(c, 20, httpapi.DefaultPageLimit)
	if !ok {
		return
	}
	out, err := a.Query.Mine(c.Request.Context(), id, persist.PageQuery{Page: page, PageSize: size, Status: c.Query("status")}, c.Query("only_usable") == "true")
	if err != nil {
		var input *marketing.InputError
		if errors.As(err, &input) {
			httpapi.BadRequest(c, input.Message)
		} else {
			httpapi.Write(c, 503, 5003, "优惠券暂时无法读取", nil)
		}
		return
	}
	httpapi.OK(c, out)
}
