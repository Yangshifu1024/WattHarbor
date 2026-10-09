package marketing

import (
	adminhttp "chargepilot/internal/http/admin"
	"chargepilot/internal/marketing"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// registerCoupons 注册优惠券模板、发放统计和人工发券接口，并校验各自权限。
func (a API) registerCoupons(r *gin.Engine) {
	r.GET("/api/v1/admin/coupons", a.Auth.Require("coupon.read"), a.coupons)
	r.POST("/api/v1/admin/coupons", a.Auth.Require("coupon.create"), a.createCoupon)
	r.PUT("/api/v1/admin/coupons/:id", a.Auth.Require("coupon.update"), a.updateCoupon)
	r.GET("/api/v1/admin/coupons/:id/stats", a.Auth.Require("coupon.read"), a.couponStats)
	r.POST("/api/v1/admin/coupons/:id/grants", a.Auth.Require("coupon.grant"), a.grantCoupon)
}

// coupons 返回优惠券模板列表。
func (a API) coupons(c *gin.Context) {
	rows, err := a.Coupons.List(c.Request.Context(), actor(c))
	if err != nil {
		failure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// createCoupon 创建优惠券模板并返回模板资料。
func (a API) createCoupon(c *gin.Context) {
	var in marketing.Coupon
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	out, err := a.Coupons.Create(c.Request.Context(), audit(c), in)
	if err != nil {
		failure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// updateCoupon 更新指定优惠券模板。
func (a API) updateCoupon(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	var in marketing.CouponUpdate
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	out, err := a.Coupons.Update(c.Request.Context(), audit(c), id, in)
	if err != nil {
		failure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// couponStats 返回指定模板的发放和使用统计。
func (a API) couponStats(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	out, err := a.Coupons.Stats(c.Request.Context(), actor(c), id)
	if err != nil {
		failure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// grantCoupon 按 request_id 向指定用户发券，并返回发放记录。
func (a API) grantCoupon(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	var in struct {
		RequestID string            `json:"request_id"` /* 请求 UUID */
		UserID    httpapi.DecimalID `json:"user_id"`    /* 十进制用户编号 */
	}
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	out, err := a.Coupons.Grant(c.Request.Context(), audit(c), id, marketing.GrantInput{RequestID: in.RequestID, UserID: uint64(in.UserID)})
	if err != nil {
		failure(c, err)
		return
	}
	httpapi.OK(c, out)
}
