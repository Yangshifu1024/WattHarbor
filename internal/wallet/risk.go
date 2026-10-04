package wallet

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"gorm.io/gorm"
	"strconv"
	"time"
)

// RiskService 在钱包模块拥有风险审核、资金预占及解冻事务。
type RiskService struct {
	DB     *gorm.DB      // central 风控及账户连接，由进程管理
	Refund RefundService // 原充值来源及退款记录创建能力，保留同一事务
}

// RiskReviewInput 是需原样存档的复核意见。
type RiskReviewInput struct {
	Approved *bool  `json:"approved"` // 必须显式给出，nil 表示未填
	Comment  string `json:"comment"`  // 原审核依据，1–255 字
}

// RiskReleaseInput 是解冻依据，参与同请求幂等比对。
type RiskReleaseInput struct {
	Comment string `json:"comment"` /* 原解冻依据，1–255 字 */
}

// List 在 ctx 下按 status、page、size 查询风险申请，并以 actor 的实时权限计算操作资格。
// 返回列表及总数或权限／存储错误；原审核回执保持原字段，不解冻、不发起退款。
func (s RiskService) List(ctx context.Context, actor operations.Profile, status string, page, size int) ([]map[string]any, int64, error) {
	if !actor.HasPermission("finance.wallet_risk.review") {
		return nil, 0, operations.ErrForbidden
	}
	rows, total, err := (RiskRepo{DB: s.DB}).ListWalletRisks(ctx, status, page, size)
	if err != nil {
		return nil, 0, err
	}
	for _, row := range rows {
		row["review"] = riskReceipt(row["review_json"])
		row["release"] = riskReceipt(row["release_json"])
		row["can_review"] = (actor.Role == "customer_finance" || actor.Role == "customer_cs") && actor.HasPermission("finance.wallet_risk.review")
		row["freeze_linked"] = row["freeze_id"] != nil
		row["can_release"] = actor.Role == "customer_finance" && actor.HasPermission("finance.wallet_risk.release") && row["review"] != nil && row["release"] == nil && riskText(row["freeze_status"]) == "frozen" && riskText(row["trigger_rule"]) == "wallet_refund_frequency"
		delete(row, "review_json")
		delete(row, "release_json")
		delete(row, "trigger_rule")
		delete(row, "freeze_id")
	}
	return rows, total, nil
}

// riskReceipt 将驱动返回的 v 原回执解析为 JSON 值；空值保留 nil，无效 JSON 保留原值。
// 仅处理展示类型，不修改原回执，也不把解析失败当成已批准审核。
func riskReceipt(v any) any {
	if v == nil {
		return nil
	}
	text := riskText(v)
	var out any
	if json.Unmarshal([]byte(text), &out) == nil {
		return out
	}
	return v
}

// riskText 将 v 的字符串或字节投影转为文本，其他类型返回空值；无 IO 或副作用。
func riskText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return ""
	}
}

// Review 在 ctx 下处理 requestID 的风控动作，audit 提供实时账号与审计上下文，in 为原提交内容。
// 返回首次存档回执；异内容或不满足冻结关联返回冲突，权限不足返回禁止，存储失败整体回滚。
// 原请求、审核、资金预占及审计同事务；解冻不代表渠道退款完成。
// READ COMMITTED 保证等待钱包锁后读取最新的已支付候选来源，不沿用申请锁前建立的 RR 快照。
func (s RiskService) Review(ctx context.Context, audit ops.AuditContext, requestID string, in RiskReviewInput) (map[string]any, error) {
	if (audit.Actor.Role != "customer_finance" && audit.Actor.Role != "customer_cs") || !audit.Actor.HasPermission("finance.wallet_risk.review") {
		return nil, operations.ErrForbidden
	}
	if !money.ValidReviewComment(in.Comment) || in.Approved == nil {
		return nil, operations.ErrConflict
	}
	var response map[string]any
	var changes []ops.AuditChange
	store := RiskRepo{DB: s.DB}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		req, err := store.LockRefundRequest(tx, requestID)
		if err != nil {
			return err
		}
		prior, err := store.FindRiskReview(tx, req.RequestID)
		if err == nil {
			if err := money.ReviewReplay(money.PriorReview{ActorID: prior.ActorID, Approved: prior.Approved, Comment: prior.Comment}, audit.Actor.ID, *in.Approved, in.Comment); err != nil {
				return operations.ErrConflict
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
			return operations.ErrConflict
		}
		if *in.Approved {
			if err := s.Refund.ReserveInTx(tx, WalletRefundReservation{RequestID: req.RequestID, UserID: req.UserID, AmountCents: req.AmountCents, Reason: "wallet risk approved"}); err != nil {
				return err
			}
		}
		response = map[string]any{"actor_id": strconv.FormatUint(audit.Actor.ID, 10), "approved": *in.Approved, "comment": in.Comment}
		b, _ := json.Marshal(response)
		if err := store.InsertRiskReview(tx, req.RequestID, audit.Actor.ID, *in.Approved, in.Comment, string(b)); err != nil {
			return err
		}
		changes = []ops.AuditChange{{Action: "review", Target: "wallet_risk", ID: req.UserID, Before: nil, After: map[string]any{"request_id": req.RequestID, "review": response}, RequestID: req.RequestID}}
		for _, change := range changes {
			if err := ops.AppendAuditInTx(tx, audit, change); err != nil {
				return err
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return response, err
}

// Release 在 ctx 下处理 requestID 的风控动作，audit 提供实时账号与审计上下文，in 为原提交内容。
// 返回首次存档回执；异内容或不满足冻结关联返回冲突，权限不足返回禁止，存储失败整体回滚。
// 原请求、审核、资金预占及审计同事务；解冻不代表渠道退款完成。
// READ COMMITTED 保证等待钱包锁后读取用户当前状态；其他冻结仍用当前读逐行核对。
func (s RiskService) Release(ctx context.Context, audit ops.AuditContext, requestID string, in RiskReleaseInput) (map[string]any, error) {
	if audit.Actor.Role != "customer_finance" || !audit.Actor.HasPermission("finance.wallet_risk.release") {
		return nil, operations.ErrForbidden
	}
	if !money.ValidReviewComment(in.Comment) {
		return nil, operations.ErrConflict
	}
	var response map[string]any
	var changes []ops.AuditChange
	store := RiskRepo{DB: s.DB}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		req, err := store.LockRefundRequest(tx, requestID)
		if err != nil {
			return err
		}
		prior, err := store.FindRiskRelease(tx, req.RequestID)
		if err == nil {
			if err := money.ReleaseReplay(money.PriorReview{ActorID: prior.ActorID, Comment: prior.Comment}, audit.Actor.ID, in.Comment); err != nil {
				return operations.ErrConflict
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
			return operations.ErrConflict
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
			return operations.ErrConflict
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
		response = map[string]any{"actor_id": strconv.FormatUint(audit.Actor.ID, 10), "comment": in.Comment, "wallet_active": restore}
		b, _ := json.Marshal(response)
		if err := store.InsertRiskRelease(tx, req.RequestID, audit.Actor.ID, in.Comment, string(b)); err != nil {
			return err
		}
		changes = []ops.AuditChange{{Action: "release", Target: "wallet_risk", ID: req.UserID, Before: nil, After: map[string]any{"request_id": req.RequestID, "release": response}, RequestID: req.RequestID}}
		for _, change := range changes {
			if err := ops.AppendAuditInTx(tx, audit, change); err != nil {
				return err
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return response, err
}
