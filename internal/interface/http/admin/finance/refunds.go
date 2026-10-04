package finance

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/gin-gonic/gin"
	"strings"
)

// registerRefunds 注册人工退款、列表、复核和重试路由。
// 分别要求 order.refund.create、finance.refund.read、order.refund.review、finance.refund.retry 权限。
// 写入语义（建单幂等重放、双人复核、执行重投）归属 refund 家族包，本文件只做 HTTP 编排与审计。
func (a API) registerRefunds(r *gin.Engine) {
	r.POST("/api/v1/admin/orders/:id/refunds", a.Auth.Require("order.refund.create"), a.manualRefund)
	r.GET("/api/v1/admin/billing/refunds", a.Auth.Require("finance.refund.read"), a.refunds)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/approve", a.Auth.Require("order.refund.review"), a.reviewRefund)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/reject", a.Auth.Require("order.refund.review"), a.reviewRefund)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/retry", a.Auth.Require("finance.refund.retry"), a.retryRefund)
}

// asConflict 将家族包的冲突哨兵映射为后台统一的 409 语义。
//
// 这里列的是"业务上不能这么做"的拒绝，不是故障：余额不足、状态已变、重复提交。
// 漏掉任何一个，它就会掉进默认分支被报成 503，运营看到的是"服务暂不可用，
// 稍后重试"——重试并不会成功，只会把一个明确的拒绝变成无从下手的等待。
func asConflict(err error) error {
	if errors.Is(err, settlement.ErrRefundConflict) ||
		errors.Is(err, settlement.ErrInvoiceConflict) {
		return admin.ErrConflict
	}
	return err
}

// manualRefund 创建待双人复核的人工退款。同一 request_id、相同内容返回原退款单；同号不同内容返回冲突。
func (a API) manualRefund(c *gin.Context) {
	id, ok := chargehttp.AdminPathID(c)
	if !ok {
		return
	}
	var in settlement.ManualRefundRequest
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if !admin.RequestIDPattern.MatchString(in.RequestID) || in.AmountCents <= 0 || !operations.ValidText(in.Reason, 255) {
		httpapi.BadRequest(c, "请填写有效请求编号、正整数退款分值和退款原因")
		return
	}
	created, refundNo, err := a.Refunds.Request(c.Request.Context(), chargehttp.AdminAuditContext(c), id, in)

	if err != nil {
		chargehttp.AdminResourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"created": created, "refund_no": refundNo, "request_id": in.RequestID})
}

// refunds 分页返回退款记录及当前操作人的 can_approve、can_reject、can_retry。
// 仅 pending、manual_review 记录可复核，首签人不能再次签署；自动退款的执行进度放入 task。
func (a API) refunds(c *gin.Context) {
	q, ok := chargehttp.ReadAdminPage(c, "pending processing success failed rejected")
	if !ok {
		return
	}
	if len(c.Query("refund_no")) > 64 {
		httpapi.BadRequest(c, "退款单号过长")
		return
	}
	out, err := settlement.ListRefunds(c.Request.Context(), a.DB, settlement.RefundListQuery{
		Status: q.Status, RefundNo: c.Query("refund_no"), Page: q.Page, PageSize: q.PageSize,
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// reviewRefund 按路由后缀执行通过或驳回。首签进入 awaiting_second；不同审核人
// 第二签（实时身份由 refund 家族经 LookupSigner 回读授权域核对）后 approved 并安排执行；
// 驳回写入 refund_rejection 并置 rejected，无需第二签。
func (a API) reviewRefund(c *gin.Context) {
	_, ok := admin.RequireFinanceActor(c, "order.refund.review")
	if !ok {
		return
	}
	var in settlement.RefundReviewInput
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	approve := strings.HasSuffix(c.FullPath(), "/approve")
	if approve && !operations.ValidText(in.ApproveComment, 255) || !approve && !operations.ValidText(in.Reason, 255) {
		httpapi.BadRequest(c, "请填写 1–255 字审核意见")
		return
	}
	status, err := a.Refunds.Review(c.Request.Context(), chargehttp.AdminAuditContext(c), c.Param("refund_no"), approve, in)

	if err != nil {
		chargehttp.AdminResourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"review_status": status})
}

// retryRefund 将 automatic、pending/processing 退款的执行安排到当前时间，不修改金额或状态。
// 人工复核退款不可通过此入口执行。
func (a API) retryRefund(c *gin.Context) {
	var in settlement.RefundRetryInput
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if !operations.ValidText(in.Reason, 255) {
		httpapi.BadRequest(c, "请填写重试原因")
		return
	}
	_, err := a.Refunds.Retry(c.Request.Context(), chargehttp.AdminAuditContext(c), c.Param("refund_no"), in)

	if err != nil {
		chargehttp.AdminResourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"scheduled": true})
}
