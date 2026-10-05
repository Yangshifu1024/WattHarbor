package user

import (
	"net/url"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	adminhttp "github.com/ChargePilot2026/charge-pilot/internal/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// 本文件处理后台充电用户的 HTTP 参数与响应，查询编排由 identity 用例执行。

// registerChargeUsers 注册用户列表、档案和充值记录，要求 user.read 权限。
// 账号冻结和解冻由风控流程处理。
func (a API) registerChargeUsers(r *gin.Engine) {
	r.GET("/api/v1/admin/users", a.Auth.Require("user.read"), a.chargeUsers)
	r.GET("/api/v1/admin/users/:id", a.Auth.Require("user.read"), a.chargeUser)
	r.GET("/api/v1/admin/users/:id/recharges", a.Auth.Require("user.read"), a.chargeUserRecharges)
}

// chargeUsers 分页列出充电用户。
//
// 关键词匹配昵称、openid 和手机号；完整手机号精确查询，号码片段支持模糊查询。
func (a API) chargeUsers(c *gin.Context) {
	q, ok := adminhttp.ReadAdminPage(c, "active frozen")
	if !ok {
		return
	}
	out, err := a.Users.List(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), q)
	if err != nil {
		adminhttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// chargeUser 返回单个充电用户的完整档案：钱包、累计消费、最近订单、券与报障计数。
func (a API) chargeUser(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	out, err := a.Users.Detail(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), id)
	if err != nil {
		adminhttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// chargeUserRecharges 分页读取路径用户的充值支付单，查询参数不能改变用户或业务范围。
func (a API) chargeUserRecharges(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	params, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		httpapi.BadRequest(c, "查询参数格式无效")
		return
	}
	for name, values := range params {
		if (name != "page" && name != "page_size") || len(values) != 1 || values[0] == "" {
			httpapi.BadRequest(c, "仅支持单值 page 和 page_size 分页参数")
			return
		}
	}
	page, ok := adminhttp.ReadAdminPage(c, "")
	if !ok {
		return
	}
	out, err := a.Users.RechargeHistory(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), id, page)
	if err != nil {
		adminhttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}
