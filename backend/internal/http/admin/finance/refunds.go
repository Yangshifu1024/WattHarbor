package finance

import (
	"errors"
	"strings"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/http/admin"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/settlement"
	"github.com/gin-gonic/gin"
)

// registerRefunds 注册人工退款、列表、复核和重试路由。
// 分别要求 order.refund.create、finance.refund.read、order.refund.review、finance.refund.retry 权限。
// r只挂载HTTP适配；建单重放、双人复核、执行重投及审计事务由settlement拥有。
func (a API) registerRefunds(r *gin.Engine) {
	r.POST("/api/v1/admin/orders/:id/refunds", a.Auth.Require("order.refund.create"), a.manualRefund)
	r.GET("/api/v1/admin/billing/refunds", a.Auth.Require("finance.refund.read"), a.refunds)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/approve", a.Auth.Require("order.refund.review"), a.reviewRefund)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/reject", a.Auth.Require("order.refund.review"), a.reviewRefund)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/retry", a.Auth.Require("finance.refund.retry"), a.retryRefund)
}

// asConflict 将err中的退款或发票冲突转为后台409哨兵，其余错误原样返回。
func asConflict(err error) error {
	if errors.Is(err, settlement.ErrRefundConflict) ||
		errors.Is(err, settlement.ErrInvoiceConflict) {
		return admin.ErrConflict
	}
	return err
}

// manualRefund 创建待双人复核的人工退款。同一 request_id、相同内容返回原退款单；同号不同内容返回冲突。
func (a API) manualRefund(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	var in settlement.ManualRefundRequest
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	if !admin.RequestIDPattern.MatchString(in.RequestID) || in.AmountCents <= 0 || !operations.ValidText(in.Reason, 255) {
		httpapi.BadRequest(c, "请填写有效请求编号、正整数退款分值和退款原因")
		return
	}
	created, refundNo, err := a.Refunds.Request(c.Request.Context(), admin.AdminAuditContext(c), id, in)

	if err != nil {
		admin.AdminResourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"created": created, "refund_no": refundNo, "request_id": in.RequestID})
}

// refunds 校验c原状态、单号与分页，输出所属退款查询回执；非法输入或读取失败返回原错误。
// 实际复核与重投准入由对应写入服务核对，不以列表字段替代当前权限。
func (a API) refunds(c *gin.Context) {
	q, ok := admin.ReadAdminPage(c, "pending processing success failed rejected")
	if !ok {
		return
	}
	if len(c.Query("refund_no")) > 64 {
		httpapi.BadRequest(c, "退款单号过长")
		return
	}
	out, err := a.Refunds.List(c.Request.Context(), settlement.RefundListQuery{
		Status: q.Status, RefundNo: c.Query("refund_no"), Page: q.Page, PageSize: q.PageSize,
	})
	if err != nil {
		admin.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// reviewRefund 按路由后缀执行通过或驳回。首签进入 awaiting_second；不同审核人
// 第二签由settlement服务读取实时身份后推进approved并安排执行；
// 驳回写入 refund_rejection 并置 rejected，无需第二签。
func (a API) reviewRefund(c *gin.Context) {
	_, ok := admin.RequireFinanceActor(c, "order.refund.review")
	if !ok {
		return
	}
	var in settlement.RefundReviewInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	approve := strings.HasSuffix(c.FullPath(), "/approve")
	if approve && !operations.ValidText(in.ApproveComment, 255) || !approve && !operations.ValidText(in.Reason, 255) {
		httpapi.BadRequest(c, "请填写 1–255 字审核意见")
		return
	}
	status, err := a.Refunds.Review(c.Request.Context(), admin.AdminAuditContext(c), c.Param("refund_no"), approve, in)

	if err != nil {
		admin.AdminResourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"review_status": status})
}

// retryRefund 将 automatic、pending/processing 退款的执行安排到当前时间，不修改金额或状态。
// 人工复核退款不可通过此入口执行。
func (a API) retryRefund(c *gin.Context) {
	var in settlement.RefundRetryInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	if !operations.ValidText(in.Reason, 255) {
		httpapi.BadRequest(c, "请填写重试原因")
		return
	}
	_, err := a.Refunds.Retry(c.Request.Context(), admin.AdminAuditContext(c), c.Param("refund_no"), in)

	if err != nil {
		admin.AdminResourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"scheduled": true})
}
