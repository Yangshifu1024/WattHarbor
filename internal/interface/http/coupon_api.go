package http

import (
	"github.com/ChargePilot2026/charge-pilot/internal/marketing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

type CouponAPI struct {
	Auth    SessionAuthenticator
	Coupons marketing.CouponStore
}

func (a CouponAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/user/coupons", a.list)
}

func (a CouponAPI) list(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	rows, err := a.Coupons.AvailableCoupons(c.Request.Context(), userID)
	if err != nil {
		httpapi.Write(c, 503, 5003, "优惠券暂时无法读取", nil)
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		out = append(out, gin.H{"coupon_id": row.CouponID, "name": row.Name, "grant_id": row.GrantID,
			"discount_type": row.Type, "min_charge_cents": row.MinCharge, "expires_at": row.ExpiresAt})
	}
	httpapi.OK(c, gin.H{"items": out})
}
