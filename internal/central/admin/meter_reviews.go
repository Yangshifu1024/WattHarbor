package admin

import (
	"errors"
	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	dbstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"strings"
)

// registerMeterReviews 挂载计量核实的三个接口。列表只读，要求 finance.read；
// 提交核实和裁决核实都要求 billing.meter.review，权限在路由层就先拦一道。
func (a ResourceAPI) registerMeterReviews(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/meter-reviews", a.Auth.Require("finance.read"), a.meterReviews)
	r.POST("/api/v1/admin/billing/meter-reviews/:id/propose", a.Auth.Require("billing.meter.review"), a.proposeMeter)
	r.POST("/api/v1/admin/billing/meter-reviews/:id/decide", a.Auth.Require("billing.meter.review"), a.decideMeter)
	r.POST("/api/v1/admin/billing/meter-reviews/:id/resolve-amount", a.Auth.Require("billing.meter.review"), a.resolveMeterAmount)
}

func (a ResourceAPI) resolveMeterAmount(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	p, ok := a.financeActor(c, "billing.meter.review")
	if !ok {
		return
	}
	var in dbstore.ManualSettlement
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	in.ChargeOrderID = id
	in.ActorID = p.ID
	in.Reason = strings.TrimSpace(in.Reason)
	if err := (dbstore.BillingOrders{DB: a.Store.UserDB}).ResolveAmount(c.Request.Context(), in); err != nil {
		meterFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"queued": true})
}

// meterReviews 按 pending、resolved 状态及订单号分页查询人工定价工单，逐单补充计量核实记录。
func (a ResourceAPI) meterReviews(c *gin.Context) {
	q, ok := chargehttp.ReadAdminPage(c, "pending resolved")
	if !ok {
		return
	}
	out, err := dbstore.ListManualFeeReviews(c.Request.Context(), a.Store.BillingDB, q)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	for i := range out.Items {
		publicSource, err := httpapi.DecimalizeIDField(out.Items[i].SourceJSON, "user_id")
		if err != nil {
			chargehttp.AdminResourceFailure(c, err)
			return
		}
		out.Items[i].SourceJSON = publicSource
		reviews, err := dbstore.ListOrderMeterReviews(c.Request.Context(), a.Store.UserDB, out.Items[i].ChargeOrderID)
		if err != nil {
			chargehttp.AdminResourceFailure(c, err)
			return
		}
		out.Items[i].Reviews = reviews
	}
	httpapi.OK(c, out)

}

// Preserve the exact persisted billing payload; only its public copy uses a
// string user ID. Internal receipts still decode historical numeric IDs.
// meterFailure 把核实相关的领域错误翻成管理端提示：状态被并发改动、原始数据对不上
// 或审核人重复归 409 并提示刷新；计量分段和价格快照的校验失败归 400；其余交给通用的
// resourceFailure 处理。
func meterFailure(c *gin.Context, err error) {
	if errors.Is(err, dbstore.ErrBillingConflict) {
		httpapi.Write(c, 409, 2009, "计量核实状态已变化、原始数据不一致或审核人重复，请刷新后重试", nil)
		return
	}
	if errors.Is(err, charging.ErrMeterReview) || errors.Is(err, charging.ErrInvalidPricing) {
		httpapi.BadRequest(c, "计量分段须连续覆盖原始时段、总电量一致，且各段不得跨不同费率；价格快照必须有效")
		return
	}
	chargehttp.AdminResourceFailure(c, err)
}

// proposeMeter 提交一次计量核实申请，path 上的 id 是兜底单的 charge_order_id。
// 分段必须连续覆盖原始时段、总电量一致、且各段不跨不同费率，上限 10080 段
// （七天按分钟切分）；request_id 必须是 UUID，用来保证重放同一次提交不会产生多条核实记录。
func (a ResourceAPI) proposeMeter(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	p, ok := a.financeActor(c, "billing.meter.review")
	if !ok {
		return
	}
	// 请求体只在此使用，不入库：核实记录由 dbstore.BillingOrders 按这里的输入落库。
	var in struct {
		RequestID string                  `json:"request_id"` // 客户端生成的 UUID 幂等键。
		Reason    string                  `json:"reason"`     // 核实依据，去空白后最长 500 字符。
		Segments  []charging.MeterSegment `json:"segments"`   // 人工修正后的计量分段，须连续覆盖原始时段且总电量与原始一致。
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if _, err := uuid.Parse(in.RequestID); err != nil || !operations.ValidText(in.Reason, 500) || len(in.Segments) == 0 || len(in.Segments) > 10080 {
		httpapi.BadRequest(c, "请填写 UUID、核实依据及有效计量分段")
		return
	}
	row, err := (dbstore.BillingOrders{DB: a.Store.UserDB}).ProposeMeter(c.Request.Context(), id, p.ID, in.RequestID, in.Reason, in.Segments)
	if err != nil {
		meterFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// decideMeter 校验核实记录所属订单，并用 Approve 指针区分缺失字段与拒绝。
// 拒绝必须填写原因；通过前复查首次核实人的账号和权限是否仍有效。
func (a ResourceAPI) decideMeter(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	p, ok := a.financeActor(c, "billing.meter.review")
	if !ok {
		return
	}
	// review_id 指定待裁决的 charge_meter_review 记录。
	var in struct {
		ReviewID uint64 `json:"review_id"` // 待裁决的核实记录主键，必须属于本订单。
		Approve  *bool  `json:"approve"`   // 是否通过；指针是为了把"没传"和 false 区分开，拒绝时必须另填原因。
		Reason   string `json:"reason"`    // 拒绝原因，去空白后最长 500 字符；通过时可留空。
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.ReviewID == 0 || in.Approve == nil || (!*in.Approve && !operations.ValidText(in.Reason, 500)) {
		httpapi.BadRequest(c, "请提供审核记录、决定和拒绝依据")
		return
	}
	review, err := dbstore.FindMeterReview(c.Request.Context(), a.Store.UserDB, in.ReviewID, id)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	if *in.Approve {
		first, err := a.Auth.Store.Profile(c.Request.Context(), review.FirstReviewerID)
		if err != nil || first.Role != "customer_finance" || !first.HasPermission("billing.meter.review") {
			httpapi.Write(c, 409, 2009, "首次核实人的账号或权限已失效，请拒绝后重新提交", nil)
			return
		}
	}
	if err := (dbstore.BillingOrders{DB: a.Store.UserDB}).ReviewMeter(c.Request.Context(), id, in.ReviewID, p.ID, *in.Approve, in.Reason); err != nil {
		meterFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"approved": *in.Approve})
}
