package marketing

import (
	"errors"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/marketing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerActivityRules 将 a 的原活动配置路径注册到 r，保留原读取／管理权限，无业务IO。
func (a API) registerActivityRules(r *gin.Engine) {
	r.GET("/api/v1/admin/coupon-activities", a.Auth.Require("coupon.activity.read"), a.listActivityRules)
	r.POST("/api/v1/admin/coupon-activities", a.Auth.Require("coupon.activity.manage"), a.createActivityRule)
	r.PUT("/api/v1/admin/coupon-activities/:id", a.Auth.Require("coupon.activity.manage"), a.updateActivityRule)
}

// listActivityRules 解析 c 原分页并调用所属列表，输出原页或映射错误，无写入。
func (a API) listActivityRules(c *gin.Context) {
	q, ok := chargehttp.ReadAdminPage(c, "active disabled")
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

// createActivityRule 绑定 c 原规则请求并调用完整创建事务，输出原主键或映射错误，不拥有事务。
func (a API) createActivityRule(c *gin.Context) {
	var in marketing.ActivityInput
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	id, err := a.Activities.Create(c.Request.Context(), audit(c), in)
	if err != nil {
		failure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// updateActivityRule 绑定 c 原路径与规则请求并调用完整修改事务，输出原主键或原缺失／映射错误。
func (a API) updateActivityRule(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var in marketing.ActivityInput
	if !chargehttp.DecodeResource(c, &in) {
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
