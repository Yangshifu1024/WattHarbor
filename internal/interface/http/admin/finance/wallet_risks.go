package finance

import (
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"strconv"
	"time"
)

// registerWalletRisks 注册风控列表、审核和解冻路由。
// 审核要求 finance.wallet_risk.review，允许客服与财务；解冻要求 finance.wallet_risk.release，仅允许财务。
func (a API) registerWalletRisks(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/wallet-risks", a.Auth.Require("finance.wallet_risk.review"), a.walletRisks)
	r.POST("/api/v1/admin/billing/wallet-risks/:request_id/review", a.Auth.Require("finance.wallet_risk.review"), a.reviewWalletRisk)
	r.POST("/api/v1/admin/billing/wallet-risks/:request_id/release", a.Auth.Require("finance.wallet_risk.release"), a.releaseWalletRisk)
}

// walletRiskStore 返回风控审核与解冻的读写入口。
func (a API) walletRiskStore() wallet.WalletRiskStore {
	return wallet.WalletRiskStore{DB: a.Store.UserDB}
}

// walletRisks 分页查询关联冻结记录的退款风控请求。
// pending、reviewed 分别表示未审核和已审核；已审核记录可按解冻状态过滤。
// can_review、can_release 按权限及审核、冻结状态计算，解冻还要求 wallet_refund_frequency 规则。
func (a API) walletRisks(c *gin.Context) {
	q, ok := chargehttp.ReadAdminPage(c, "pending reviewed")
	if !ok {
		return
	}
	rows, total, err := a.walletRiskStore().ListWalletRisks(c.Request.Context(), q.Status, q.Page, q.PageSize)
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	out := admin.Page[map[string]any]{Items: rows, Page: q.Page, PageSize: q.PageSize, Total: total}
	admin.NormalizeRows(out.Items)
	httpapi.DecimalizeRowIDsAll(out.Items, "user_id")
	p := c.MustGet("admin_profile").(operations.Profile)
	for _, row := range out.Items {
		row["can_review"] = (p.Role == "customer_finance" || p.Role == "customer_cs") && p.HasPermission("finance.wallet_risk.review")
		row["review"] = row["review_json"]
		row["release"] = row["release_json"]
		row["freeze_linked"] = row["freeze_id"] != nil
		row["can_release"] = p.Role == "customer_finance" && p.HasPermission("finance.wallet_risk.release") && row["review"] != nil && row["release"] == nil && row["freeze_status"] == "frozen" && row["trigger_rule"] == "wallet_refund_frequency"
		delete(row, "review_json")
		delete(row, "release_json")
		delete(row, "trigger_rule")
		delete(row, "freeze_id")
	}
	httpapi.OK(c, out)
}

// reviewWalletRisk 要求财务或客服角色，并验证请求恰好关联一条 wallet_refund_frequency 冻结记录。
// 通过时按原渠道拆分充值退款并冻结等额余额；拒绝时仅保存审核结论。
// 相同请求、审核人、结论和留言返回首次回执；内容不一致时返回冲突。
func (a API) reviewWalletRisk(c *gin.Context) {
	p := c.MustGet("admin_profile").(operations.Profile)
	if p.Role != "customer_finance" && p.Role != "customer_cs" {
		httpapi.Write(c, 403, 1003, "须由财务或客服风控审核人员操作", nil)
		return
	}
	var in struct {
		Approved *bool  `json:"approved"` // 审核结论，true 通过 / false 驳回；必须显式给出，指针为 nil 视为未填
		Comment  string `json:"comment"`  // 审核依据，1–255 字且不含控制字符
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if in.Approved == nil || !money.ValidReviewComment(in.Comment) {
		httpapi.BadRequest(c, "请填写审核结论及有效依据")
		return
	}
	var response map[string]any
	var auditPending []chargehttp.AuditEntry
	store := a.walletRiskStore()
	err := chargehttp.AuditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		req, err := store.LockRefundRequest(tx, c.Param("request_id"))
		if err != nil {
			return err
		}
		prior, err := store.FindRiskReview(tx, req.RequestID)
		if err == nil {
			if err := money.ReviewReplay(money.PriorReview{ActorID: prior.ActorID, Approved: prior.Approved, Comment: prior.Comment}, p.ID, *in.Approved, in.Comment); err != nil {
				return admin.ErrConflict
			}
			return json.Unmarshal([]byte(prior.ResponseJSON), &response)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		linked, err := store.CountFrequencyFreezes(tx, req.RequestID, req.UserID)
		if err != nil {
			return err
		}
		if linked != 1 {
			return admin.ErrConflict
		}
		if *in.Approved {
			if err := reserveWalletRefund(tx, req); err != nil {
				return err
			}
		}
		response = map[string]any{"actor_id": strconv.FormatUint(p.ID, 10), "approved": *in.Approved, "comment": in.Comment}
		b, _ := json.Marshal(response)
		if err := store.InsertRiskReview(tx, req.RequestID, p.ID, *in.Approved, in.Comment, string(b)); err != nil {
			return err
		}
		auditPending = []chargehttp.AuditEntry{{Action: "review", Target: "wallet_risk", ID: req.UserID, Before: nil, After: gin.H{"request_id": req.RequestID, "review": response}, RequestID: req.RequestID}}
		return nil
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, response)
}

// reserveWalletRefund 按申请金额拆分微信充值支付单，创建 automatic/pending 退款并冻结钱包余额。
// 仅使用全额实付且有微信交易号的充值，扣除已有退款占用与已退金额；资金不足时整笔事务回滚。
func reserveWalletRefund(tx *gorm.DB, req wallet.WalletRiskRequest) error {
	err := wallet.ReserveRefund(tx, wallet.WalletRefundReservation{RequestID: req.RequestID, UserID: req.UserID, AmountCents: req.AmountCents, Reason: "wallet risk approved"})
	if errors.Is(err, wallet.ErrWalletRefundConflict) {
		return admin.ErrConflict
	}
	return err
}

// releaseWalletRisk 由客户财务解除已审核、同用户且仍生效的 wallet_refund_frequency 冻结。
// 解冻后仅在用户 active 且无其他冻结时恢复钱包为 active。
// 相同审核人和留言重放返回首次回执；内容不一致时返回冲突。
func (a API) releaseWalletRisk(c *gin.Context) {
	p, ok := a.RequireFinanceActor(c, "finance.wallet_risk.release")
	if !ok {
		return
	}
	var in struct {
		Comment string `json:"comment"` // 解冻依据，1–255 字且不含控制字符
	}
	if !chargehttp.DecodeResource(c, &in) {
		return
	}
	if !money.ValidReviewComment(in.Comment) {
		httpapi.BadRequest(c, "请填写有效解冻依据")
		return
	}
	var response map[string]any
	var auditPending []chargehttp.AuditEntry
	store := a.walletRiskStore()
	err := chargehttp.AuditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		req, err := store.LockRefundRequest(tx, c.Param("request_id"))
		if err != nil {
			return err
		}
		prior, err := store.FindRiskRelease(tx, req.RequestID)
		if err == nil {
			if err := money.ReleaseReplay(money.PriorReview{ActorID: prior.ActorID, Comment: prior.Comment}, p.ID, in.Comment); err != nil {
				return admin.ErrConflict
			}
			return json.Unmarshal([]byte(prior.ResponseJSON), &response)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		count, err := store.CountRiskReviews(tx, req.RequestID)
		if err != nil {
			return err
		}
		if count != 1 {
			return admin.ErrConflict
		}
		w, err := store.LockWallet(tx, req.UserID)
		if err != nil {
			return err
		}
		freeze, err := store.LockFreeze(tx, req.RequestID)
		if err != nil {
			return err
		}
		if freeze.UserID != req.UserID || freeze.TriggerRule != "wallet_refund_frequency" || freeze.Status != "frozen" {
			return admin.ErrConflict
		}
		if err := store.Unfreeze(tx, freeze.ID, time.Now().UTC()); err != nil {
			return err
		}
		active, err := store.CountActiveFreezes(tx, req.UserID)
		if err != nil {
			return err
		}
		userStatus, err := store.UserStatus(tx, req.UserID)
		if err != nil {
			return err
		}
		restore := active == 0 && userStatus == "active"
		if restore {
			if err := store.ActivateWallet(tx, w.ID); err != nil {
				return err
			}
		}
		response = map[string]any{"actor_id": strconv.FormatUint(p.ID, 10), "comment": in.Comment, "wallet_active": restore}
		b, _ := json.Marshal(response)
		if err := store.InsertRiskRelease(tx, req.RequestID, p.ID, in.Comment, string(b)); err != nil {
			return err
		}
		auditPending = []chargehttp.AuditEntry{{Action: "release", Target: "wallet_risk", ID: req.UserID, Before: nil, After: gin.H{"request_id": req.RequestID, "release": response}, RequestID: req.RequestID}}
		return nil
	})
	if err != nil {
		chargehttp.AdminResourceFailure(c, err)
		return
	}
	httpapi.OK(c, response)
}
