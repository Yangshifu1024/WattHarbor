package marketing

import (
	"errors"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	adminhttp "wattharbor/internal/http/admin"
	"wattharbor/internal/marketing"
	"wattharbor/internal/platform/httpapi"
)

// registerActivityRules 注册优惠券活动规则查询、创建和修改接口。
func (a API) registerActivityRules(r *gin.Engine) {
	r.GET("/api/v1/admin/coupon-activities", a.Auth.Require("coupon.activity.read"), a.listActivityRules)
	r.POST("/api/v1/admin/coupon-activities", a.Auth.Require("coupon.activity.manage"), a.createActivityRule)
	r.PUT("/api/v1/admin/coupon-activities/:id", a.Auth.Require("coupon.activity.manage"), a.updateActivityRule)
}

// listActivityRules 分页查询活动规则。
func (a API) listActivityRules(c *gin.Context) {
	q, ok := adminhttp.ReadAdminPage(c, "active disabled")
	if !ok {
		return
	}
	out, err := a.Activities.List(c.Request.Context(), actor(c), q)
	if err != nil {
		failure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// createActivityRule 创建活动规则并返回 ID。
func (a API) createActivityRule(c *gin.Context) {
	var in marketing.ActivityInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	id, err := a.Activities.Create(c.Request.Context(), audit(c), in)
	if err != nil {
		failure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// updateActivityRule 更新指定活动规则并返回 ID。
func (a API) updateActivityRule(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	var in marketing.ActivityInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	err := a.Activities.Update(c.Request.Context(), audit(c), id, in)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "活动规则不存在", nil)
		return
	}
	if err != nil {
		failure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}
