package http

import (
	"github.com/ChargePilot2026/charge-pilot/internal/marketing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// CouponAPI 仅适配原用户可用券读取，不发放或核销优惠券。
type CouponAPI struct {
	Auth  SessionAuthenticator         // 原当前用户会话准入
	Query marketing.CouponQueryService // 所属用户券查询
}

// Register 将 a 原可用券路径注册到 r，无业务IO。
func (a CouponAPI) Register(r *gin.Engine) { r.GET("/api/v1/user/coupons", a.list) }

// list 核对 c 原用户凭据并调用所属查询，输出原精简券数组或读取错误，无写入。
func (a CouponAPI) list(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	rows, err := a.Query.Available(c.Request.Context(), userID)
	if err != nil {
		httpapi.Write(c, 503, 5003, "优惠券暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}
