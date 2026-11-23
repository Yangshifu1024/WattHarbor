package finance

import (
	"chargepilot/internal/domain/money"
	"errors"
	"net/http"
	"strings"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/http/admin"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/settlement"
	"github.com/gin-gonic/gin"
)

// registerRefunds 注册人工退款申请、列表、审核和重试接口，并校验各自权限。
func (a API) registerRefunds(r *gin.Engine) {
	r.GET("/api/v1/admin/orders/:id/refunds", a.Auth.Require("finance.refund.read"), a.orderRefunds)
	r.POST("/api/v1/admin/orders/:id/refunds", a.Auth.Require("order.refund.create"), a.manualRefund)
	r.GET("/api/v1/admin/billing/refunds", a.Auth.Require("finance.refund.read"), a.refunds)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/approve", a.Auth.Require("order.refund.review"), a.reviewRefund)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/reject", a.Auth.Require("order.refund.review"), a.reviewRefund)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/retry", a.Auth.Require("finance.refund.retry"), a.retryRefund)
}

// orderRefunds 返回指定订单现有退款，不创建退款或触发审核。
func (a API) orderRefunds(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	items, err := a.Refunds.ListForOrder(c.Request.Context(), c.MustGet("admin_profile").(operations.Profile), id)
	if errors.Is(err, operations.ErrForbidden) {
		httpapi.Write(c, http.StatusForbidden, 1003, "当前权限不足", nil)
		return
	}
	if err != nil {
		admin.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"order_id": id, "items": items, "total": len(items)})
}

// asConflict 将 err 中的退款或发票冲突转为后台 409 对应的错误类型，其余错误原样返回。
func asConflict(err error) error {
	if errors.Is(err, settlement.ErrRefundConflict) || errors.Is(err, settlement.ErrRefundNotSettled) ||
		errors.Is(err, settlement.ErrInvoiceConflict) {
		return admin.ErrConflict
	}
	return err
}

// manualRefund 创建待双人复核的人工退款。同一 request_id、相同内容返回退款单；同号不同内容返回冲突。
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

	if errors.Is(err, settlement.ErrRefundNotSettled) {
		httpapi.Write(c, http.StatusConflict, 2000, "订单尚未完成费用结算，请结算后再申请退款", nil)
		return
	}
	if err != nil {
		admin.AdminResourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"created": created, "refund_no": refundNo, "request_id": in.RequestID})
}

// refunds 按状态、退款单号和分页查询退款记录。
func (a API) refunds(c *gin.Context) {
	q, ok := admin.ReadAdminPage(c, money.AllRefundStatuses)
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

// reviewRefund 按路由执行通过或驳回。第一次通过后返回 awaiting_second；第二位审核人通过后，
// 将执行策略改为 automatic 并安排退款，接口返回 approved。驳回只需一人，退款状态改为 rejected。
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
