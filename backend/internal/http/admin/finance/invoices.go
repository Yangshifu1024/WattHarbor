package finance

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/http/admin"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/settlement"
	"github.com/gin-gonic/gin"
)

// registerInvoices 挂载开票审核的三个接口。列表只读，要求 finance.read；审批和驳回
// 共用审核处理器，由 r 注册的路由路径区分动作，两者都要求 invoice.review。
func (a API) registerInvoices(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/invoices", a.Auth.Require("finance.read"), a.invoices)
	r.POST("/api/v1/admin/billing/invoices/:id/approve", a.Auth.Require("invoice.review"), a.reviewInvoice)
	r.POST("/api/v1/admin/billing/invoices/:id/reject", a.Auth.Require("invoice.review"), a.reviewInvoice)
}

// invoices 返回开票申请列表，左连 invoice_admin_review 汇总双审进度。带 queue_ 前缀
// 的三个字段是汇总后的当前值（优先取审核表，没有再回退到申请单本身），因为审核进度
// 可能只记在审核表里。审核人 ID 统一转成字符串输出，避免 JS 大整数精度丢失。
func (a API) invoices(c *gin.Context) {
	rows, err := a.Invoices.List(c.Request.Context())
	if err != nil {
		admin.AdminResourceFailure(c, err)
		return
	}
	admin.NormalizeRows(rows)
	httpapi.DecimalizeRowIDsAll(rows, "user_id", "first_reviewer_id", "second_reviewer_id")
	httpapi.OK(c, gin.H{"items": rows})
}

// reviewInvoice 从 c 适配审批和驳回的输入、权限及错误，输出审核状态。
// 双审、申请单状态和审计写入的完整事务由 settlement 服务拥有。
func (a API) reviewInvoice(c *gin.Context) {
	id, ok := admin.AdminPathID(c)
	if !ok {
		return
	}
	_, ok = admin.RequireFinanceActor(c, "invoice.review")
	if !ok {
		return
	}
	// 请求体随动作而变：审批只读 invoice_url，驳回只读 reason，两者的必填项由下方校验区分。
	var in settlement.InvoiceReviewInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	approve := c.FullPath() == "/api/v1/admin/billing/invoices/:id/approve"
	if (approve && !operations.ValidPublicURL(in.InvoiceURL)) || (!approve && !operations.ValidText(in.Reason, 255)) {
		httpapi.BadRequest(c, "请填写 HTTPS 发票链接或拒绝原因")
		return
	}
	status, err := a.Invoices.Review(c.Request.Context(), admin.AdminAuditContext(c), id, approve, in)

	if err != nil {
		admin.AdminResourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"review_status": status})
}
