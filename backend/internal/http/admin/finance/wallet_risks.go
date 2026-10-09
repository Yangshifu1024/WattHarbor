package finance

import (
	"errors"

	"chargepilot/internal/domain/money"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/http/admin"
	"chargepilot/internal/persist"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/wallet"
	"github.com/gin-gonic/gin"
)

// registerWalletRisks 注册风控列表、审核和解冻路由。
// 审核要求 finance.wallet_risk.review，允许客服与财务；解冻要求 finance.wallet_risk.release，仅允许财务。
func (a API) registerWalletRisks(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/wallet-risks", a.Auth.Require("finance.wallet_risk.review"), a.walletRisks)
	r.POST("/api/v1/admin/billing/wallet-risks/:request_id/review", a.Auth.Require("finance.wallet_risk.review"), a.reviewWalletRisk)
	r.POST("/api/v1/admin/billing/wallet-risks/:request_id/release", a.Auth.Require("finance.wallet_risk.release"), a.releaseWalletRisk)
}

// walletRisks 分页查询关联冻结记录的退款风控请求。
// pending、reviewed 分别表示未审核和已审核；已审核记录可按解冻状态过滤。
// can_review、can_release 按权限及审核、冻结状态计算，解冻还要求 wallet_refund_frequency 规则。
func (a API) walletRisks(c *gin.Context) {
	q, ok := admin.ReadAdminPage(c, "pending reviewed")
	if !ok {
		return
	}
	p := c.MustGet("admin_profile").(operations.Profile)
	rows, total, err := a.Risks.List(c.Request.Context(), p, q.Status, q.Page, q.PageSize)
	if err != nil {
		admin.AdminResourceFailure(c, err)
		return
	}
	out := persist.Page[map[string]any]{Items: rows, Page: q.Page, PageSize: q.PageSize, Total: total}
	admin.NormalizeRows(out.Items)
	httpapi.DecimalizeRowIDsAll(out.Items, "user_id")
	httpapi.OK(c, out)
}

// reviewWalletRisk 要求财务或客服角色，并验证请求恰好关联一条 wallet_refund_frequency 冻结记录。
// 通过时按渠道拆分充值退款并冻结等额余额；拒绝时仅保存审核结论。
// 相同请求、审核人、结论和留言返回首次回执；内容不一致时返回冲突。
func (a API) reviewWalletRisk(c *gin.Context) {
	p := c.MustGet("admin_profile").(operations.Profile)
	if p.Role != "customer_finance" && p.Role != "customer_cs" {
		httpapi.Write(c, 403, 1003, "须由财务或客服风控审核人员操作", nil)
		return
	}
	var in wallet.RiskReviewInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	if in.Approved == nil || !money.ValidReviewComment(in.Comment) {
		httpapi.BadRequest(c, "请填写审核结论及有效依据")
		return
	}
	response, err := a.Risks.Review(c.Request.Context(), admin.AdminAuditContext(c), c.Param("request_id"), in)

	if err != nil {
		admin.AdminResourceFailure(c, riskFailure(err))
		return
	}
	httpapi.OK(c, response)
}

// releaseWalletRisk 由客户财务解除已审核、同用户且仍生效的 wallet_refund_frequency 冻结。
// 解冻后仅在用户 active 且无其他冻结时恢复钱包为 active。
// 相同审核人和留言重放返回首次回执；内容不一致时返回冲突。
func (a API) releaseWalletRisk(c *gin.Context) {
	_, ok := admin.RequireFinanceActor(c, "finance.wallet_risk.release")
	if !ok {
		return
	}
	var in wallet.RiskReleaseInput
	if !httpapi.DecodeResource(c, &in) {
		return
	}
	if !money.ValidReviewComment(in.Comment) {
		httpapi.BadRequest(c, "请填写有效解冻依据")
		return
	}
	response, err := a.Risks.Release(c.Request.Context(), admin.AdminAuditContext(c), c.Param("request_id"), in)

	if err != nil {
		admin.AdminResourceFailure(c, riskFailure(err))
		return
	}
	httpapi.OK(c, response)
}

// riskFailure 将钱包预占业务冲突映射到后台冲突契约。
func riskFailure(err error) error {
	if errors.Is(err, wallet.ErrWalletRefundConflict) {
		return admin.ErrConflict
	}
	return err
}
