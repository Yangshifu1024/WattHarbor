package marketing

import (
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/marketing"
	"github.com/gin-gonic/gin"
)

// API 将本业务族的用户请求交给所属用例，字段由 cmd 装配。
type API struct {
	Query marketing.CouponQueryService  // 读取当前用户优惠券及可用状态。
	Auth  authhttp.SessionAuthenticator // 验证当前会话与用户状态；失败已写响应，处理器立即返回。
}

// Register 向 r 注册本业务族的原用户路由；只装配处理器，不执行查询或写入。
func (a API) Register(r *gin.Engine) {
	r.GET("/api/v1/user/coupon/my", a.myCoupons)
	r.GET("/api/v1/user/coupons", a.list)
}
