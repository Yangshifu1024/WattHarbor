package marketing

import (
	adminhttp "chargepilot/internal/http/admin"
	"chargepilot/internal/marketing"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// registerCoupons 将 a 券模板、统计与人工发放路径注册到 r，分别保留权限。
func (a API) registerCoupons(r *gin.Engine) {
	r.GET("/api/v1/admin/coupons", a.Auth.Require("coupon.read"), a.coupons)
	r.POST("/api/v1/admin/coupons", a.Auth.Require("coupon.create"), a.createCoupon)
	r.PUT("/api/v1/admin/coupons/:id", a.Auth.Require("coupon.update"), a.updateCoupon)
	r.GET("/api/v1/admin/coupons/:id/stats", a.Auth.Require("coupon.read"), a.couponStats)
	r.POST("/api/v1/admin/coupons/:id/grants", a.Auth.Require("coupon.grant"), a.grantCoupon)
}

// coupons 将 c 当前操作人传给模板查询，输出数组或映射错误，不写库。
func (a API) coupons(c *gin.Context) {
	rows, err := a.Coupons.List(c.Request.Context(), actor(c))
	if err != nil {
		failure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// createCoupon 绑定 c 模板输入并交给用例，输出创建模型或映射错误，不拥有事务。
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

// updateCoupon 绑定 c 路径与允许修改字段并调用用例，输出字段或映射错误，不拥有事务。
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

// couponStats 将 c 路径及操作人传给统计读取，输出计数或映射错误，不写库。
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

// grantCoupon 适配 c 十进制用户与请求号并调用发放事务，输出凭据或映射错误。
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
