package order

import (
	"strconv"
	"time"

	"chargepilot/internal/domain/operations"
	adminhttp "chargepilot/internal/http/admin"
	orderstore "chargepilot/internal/order"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// 本文件只保留后台订单输入、身份适配与公开响应，基础查询由 order.QueryService 执行。

// orders 解析 c 中分页、筛选和后台身份，输出原订单页或公开输入／权限／读取错误；不写业务数据。
func (a API) orders(c *gin.Context) {
	page, ok := adminhttp.ReadAdminPage(c, "pending_payment paid charging completed cancelled failed refunding refunded")
	if !ok {
		return
	}
	q := orderstore.OrderQuery{PageQuery: page, OrderNo: c.Query("order_no"), DeviceID: c.Query("device_id"),
		BusinessStatus: c.Query("business_status"), PaymentStatus: c.Query("payment_status"), StartSource: c.Query("start_source")}
	if !adminhttp.AdminOneOf(q.BusinessStatus, "pending_start charging completed") || !adminhttp.AdminOneOf(q.PaymentStatus, "pending paid refunded partial_refunded") || !adminhttp.AdminOneOf(q.StartSource, "payment balance card") {
		httpapi.BadRequest(c, "业务状态、支付状态或启动来源无效")
		return
	}
	if len(q.OrderNo) > 64 || len(q.DeviceID) > 64 {
		httpapi.BadRequest(c, "订单号或设备号过长")
		return
	}
	if raw := c.Query("station_id"); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || id == 0 {
			httpapi.BadRequest(c, "站点 ID 无效")
			return
		}
		q.StationID = id
	}
	for key, target := range map[string]**time.Time{"started_from": &q.From, "started_to": &q.To} {
		if raw := c.Query(key); raw != "" {
			v, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				httpapi.BadRequest(c, "时间格式须为 ISO 8601")
				return
			}
			*target = &v
		}
	}
	if q.From != nil && q.To != nil && q.From.After(*q.To) {
		httpapi.BadRequest(c, "开始时间不能晚于结束时间")
		return
	}
	out, err := a.Query.List(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), q)
	if err != nil {
		adminhttp.AdminResourceFailure(c, err)
		return
	}
	a.Live.Fill(c.Request.Context(), out.Items)
	httpapi.OK(c, out)
}

// order 返回订单详情、计费单号及分账汇总。
// 仅对持有 order.refund.create 权限的操作人回填 RefundApplicantID，供界面显示退款入口；后端另行校验退款权限。
func (a API) order(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	row, err := a.Query.Detail(ctx, c.MustGet("admin_profile").(operations.Profile), id)
	if err != nil {
		adminhttp.AdminResourceFailure(c, err)
		return
	}
	rows := []orderstore.OrderView{row}
	a.Live.Fill(ctx, rows)
	httpapi.OK(c, rows[0])
}

// timeline 是 GET /api/v1/admin/orders/：id/timeline 的处理函数：
// 按发生时间顺序返回该订单的事件日志，用于还原一次充电从下单到结束的完整过程。
// 先确认订单存在再查日志，避免对不存在的订单返回空列表。
func (a API) timeline(c *gin.Context) {
	id, ok := adminhttp.AdminPathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	rows, err := a.Query.Timeline(ctx, c.MustGet("admin_profile").(operations.Profile), id)
	if err != nil {
		adminhttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"order_id": id, "timeline": rows})
}
