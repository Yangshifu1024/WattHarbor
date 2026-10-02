package admin

import (
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"net/url"

	"github.com/gin-gonic/gin"
)

// 本文件只保留后台充电用户的 HTTP 处理，读模型与查询在 database 包。

// registerChargeUsers 注册用户列表、档案和充值记录，要求 user.read 权限。
// 账号冻结和解冻由风控流程处理。
func (a ResourceAPI) registerChargeUsers(r *gin.Engine) {
	r.GET("/api/v1/admin/users", a.Auth.Require("user.read"), a.chargeUsers)
	r.GET("/api/v1/admin/users/:id", a.Auth.Require("user.read"), a.chargeUser)
	r.GET("/api/v1/admin/users/:id/recharges", a.Auth.Require("user.read"), a.chargeUserRecharges)
}

// chargeUsers 分页列出充电用户。
//
// 关键词匹配昵称、openid 和手机号；完整手机号精确查询，号码片段支持模糊查询。
func (a ResourceAPI) chargeUsers(c *gin.Context) {
	q, ok := chargehttp.ReadAdminPage(c, "active frozen")
	if !ok {
		return
	}
	rows, total, err := database.ChargeUsers(c.Request.Context(), a.Store.UserDB, q)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	if err := database.DecorateChargeUsers(c.Request.Context(), a.Store.UserDB, rows); err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, database.Page[database.ChargeUserRow]{Items: rows, Total: total, Page: q.Page, PageSize: q.PageSize,
		Permissions: c.MustGet("admin_profile").(operations.Profile).Permissions})
}

// chargeUser 返回单个充电用户的完整档案：钱包、累计消费、最近订单、券与报障计数。
func (a ResourceAPI) chargeUser(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	detail, err := database.LoadChargeUserDetail(c.Request.Context(), a.Store.UserDB, id, database.ChargeUserDetailOrderLimit)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	row := []database.ChargeUserRow{detail.ChargeUserRow}
	if err := database.DecorateChargeUsers(c.Request.Context(), a.Store.UserDB, row); err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	detail.ChargeUserRow = row[0]
	httpapi.OK(c, detail)
}

// chargeUserRecharges 分页读取路径用户的充值支付单，查询参数不能改变用户或业务范围。
func (a ResourceAPI) chargeUserRecharges(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
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
	page, ok := chargehttp.ReadAdminPage(c, "")
	if !ok {
		return
	}
	out, err := database.ChargeUserRecharges(c.Request.Context(), a.Store.UserDB, id, page)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}
