package device

import (
	"errors"
	"strings"

	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/http/admin"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/settlement"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// registerMeterReviews 挂载计量核实的三个接口。列表只读，要求 finance.read；
// 提交核实和裁决核实均要求 billing.meter.review，r 挂载路由层鉴权，服务继续核对实时权限。
func (a API) registerMeterReviews(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/meter-reviews", a.Auth.Require("finance.read"), a.meterReviews)
	r.POST("/api/v1/admin/billing/meter-reviews/:id/propose", a.Auth.Require("billing.meter.review"), a.proposeMeter)
	r.POST("/api/v1/admin/billing/meter-reviews/:id/decide", a.Auth.Require("billing.meter.review"), a.decideMeter)
	r.POST("/api/v1/admin/billing/meter-reviews/:id/resolve-amount", a.Auth.Require("billing.meter.review"), a.resolveMeterAmount)
}

// resolveMeterAmount 绑定 c 的最终金额裁决，覆盖订单与操作人身份后调用结算用例并返回排队回执。
func (a API) resolveMeterAmount(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	_, ok = admin.RequireFinanceActor(c, "billing.meter.review")
	if !ok {
		return
	}
	var in settlement.ManualSettlement
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	in.ChargeOrderID = id

	in.Reason = strings.TrimSpace(in.Reason)
	if err := a.Meters.ResolveAmount(c.Request.Context(), admin.AdminAuditContext(c), id, in); err != nil {
		meterFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"queued": true})
}

// meterReviews 解析 c 的 pending／resolved 与分页筛选；用例批量补充本页计量核实，接口仅转换公开身份 JSON。
func (a API) meterReviews(c *gin.Context) {
	q, ok := admin.ReadAdminPage(c, "pending resolved")
	if !ok {
		return
	}
	actor := c.MustGet("admin_profile").(operations.Profile)
	out, err := a.Meters.List(c.Request.Context(), actor, q)
	if err != nil {
		admin.AdminResourceFailure(c, err)
		return
	}
	for i := range out.Items {
		publicSource, err := httpapi.DecimalizeIDField(out.Items[i].SourceJSON, "user_id")
		if err != nil {
			admin.AdminResourceFailure(c, err)
			return
		}
		out.Items[i].SourceJSON = publicSource
	}
	httpapi.OK(c, out)

}

// meterFailure 把核实相关的领域错误翻成管理端提示：状态被并发改动、原始数据对不上
// 或审核人重复归 409 并提示刷新；计量分段和价格快照的校验失败归 400；其余交给通用的
// resourceFailure 处理。
func meterFailure(c *gin.Context, err error) {
	if errors.Is(err, settlement.ErrMeterSignerUnavailable) {
		httpapi.Write(c, 409, 2009, "首次核实人的账号或权限已失效，请拒绝后重新提交", nil)
		return
	}
	if errors.Is(err, settlement.ErrBillingConflict) {
		httpapi.Write(c, 409, 2009, "计量核实状态已变化、原始数据不一致或审核人重复，请刷新后重试", nil)
		return
	}
	if errors.Is(err, charging.ErrMeterReview) || errors.Is(err, charging.ErrInvalidPricing) {
		httpapi.BadRequest(c, "计量分段须连续覆盖原始时段、总电量一致，且各段不得跨不同费率；价格快照必须有效")
		return
	}
	admin.AdminResourceFailure(c, err)
}

// proposeMeter 从 c 提交计量核实申请，路径 id 是待核实订单的 charge_order_id。
// 分段必须连续覆盖原始时段、总电量一致、且各段不跨不同费率，上限 10080 段
// （七天按分钟切分）；request_id 必须是 UUID，用来保证重放同一次提交不会产生多条核实记录。
func (a API) proposeMeter(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	_, ok = admin.RequireFinanceActor(c, "billing.meter.review")
	if !ok {
		return
	}
	// MeterReviewService 保存输入及核实回执，HTTP 不直接写核实记录。
	var in settlement.MeterProposalInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if _, err := uuid.Parse(in.RequestID); err != nil || !operations.ValidText(in.Reason, 500) || len(in.Segments) == 0 || len(in.Segments) > 10080 {
		httpapi.BadRequest(c, "请填写 UUID、核实依据及有效计量分段")
		return
	}
	row, err := a.Meters.Propose(c.Request.Context(), admin.AdminAuditContext(c), id, in)
	if err != nil {
		meterFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// decideMeter 校验核实记录订单，并用 Approve 指针区分缺失字段与拒绝。
// 拒绝必须填写原因；通过前复查首次核实人的账号和权限是否仍有效。
func (a API) decideMeter(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	_, ok = admin.RequireFinanceActor(c, "billing.meter.review")
	if !ok {
		return
	}
	// review_id 指定待裁决的 charge_meter_review 记录。
	var in settlement.MeterDecisionInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.ReviewID == 0 || in.Approve == nil || (!*in.Approve && !operations.ValidText(in.Reason, 500)) {
		httpapi.BadRequest(c, "请提供审核记录、决定和拒绝依据")
		return
	}
	if err := a.Meters.Decide(c.Request.Context(), admin.AdminAuditContext(c), id, in); err != nil {
		meterFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"approved": *in.Approve})
}
