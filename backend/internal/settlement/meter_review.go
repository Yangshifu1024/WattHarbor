package settlement

import (
	"context"
	"errors"
	"strings"

	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/persist"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrMeterSignerUnavailable 表示首次核实人的当前财务身份或权限已失效。
var ErrMeterSignerUnavailable = errors.New("meter first signer unavailable")

// MeterReviewService 负责计量核实和人工金额的权限校验、审计和事务。
type MeterReviewService struct {
	Orders         BillingOrders                                         // 计费任务、计量快照及决策的事务能力
	LookupReviewer func(context.Context, uint64) (ReviewerStatus, error) // cmd 注入的实时首签人身份查询
}

// MeterProposalInput 保存计量修正提案和请求号。
type MeterProposalInput struct {
	RequestID string                  `json:"request_id"` // 客户端 UUID，同内容复用核实记录
	Reason    string                  `json:"reason"`     // 人工依据，规范化后 1–500 字
	Segments  []charging.MeterSegment `json:"segments"`   // 有单位及分段约束，不补造计量
}

// MeterDecisionInput 指定本订单核实记录和通过／驳回意见。
type MeterDecisionInput struct {
	ReviewID uint64 `json:"review_id"` // 必须属于当前订单的已保存记录主键
	Approve  *bool  `json:"approve"`   // 必须显式提交，nil 表示缺失
	Reason   string `json:"reason"`    // 驳回依据，规范化后 1–500 字
}

// List 核对财务读取权限后分页读取核实队列和每单全部核实记录，最多三次查询。
// 公开 ID 格式由 HTTP 层转换。
func (s MeterReviewService) List(ctx context.Context, actor operations.Profile, q persist.PageQuery) (persist.Page[ManualFeeReviewListRow], error) {
	if !actor.HasPermission("finance.read") {
		return persist.Page[ManualFeeReviewListRow]{}, operations.ErrForbidden
	}
	out, err := ListManualFeeReviews(ctx, s.Orders.DB, q)
	if err != nil {
		return out, err
	}
	ids := make([]uint64, len(out.Items))
	for i, row := range out.Items {
		ids[i] = row.ChargeOrderID
	}
	reviews, err := ListMeterReviewsByOrder(ctx, s.Orders.DB, ids)
	if err != nil {
		return out, err
	}
	for i := range out.Items {
		out.Items[i].Reviews = reviews[out.Items[i].ChargeOrderID]
		if out.Items[i].Reviews == nil {
			out.Items[i].Reviews = []MeterReview{}
		}
	}
	return out, nil
}

// permitted 用 audit 的实时账号核对财务角色及计量复核权限，不依赖 HTTP。
func (s MeterReviewService) permitted(audit ops.AuditContext) bool {
	return audit.Actor.Role == "customer_finance" && audit.Actor.HasPermission("billing.meter.review")
}

// Propose 锁定计费任务后提交计量提案，核对重放；计量快照、提案和首次审计同事务。
func (s MeterReviewService) Propose(ctx context.Context, audit ops.AuditContext, id uint64, in MeterProposalInput) (MeterReview, error) {
	if !s.permitted(audit) {
		return MeterReview{}, operations.ErrForbidden
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if uuid.Validate(in.RequestID) != nil || !operations.ValidText(in.Reason, 500) || len(in.Segments) == 0 || len(in.Segments) > 10080 {
		return MeterReview{}, ErrBillingConflict
	}
	var out MeterReview
	err := s.Orders.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := LockMeterReviewJob(tx, id); err != nil {
			return err
		}
		prior, err := MeterProposalExists(tx, in.RequestID)
		if err != nil {
			return err
		}
		out, err = (BillingOrders{DB: tx}).ProposeMeter(ctx, id, audit.Actor.ID, in.RequestID, in.Reason, in.Segments)
		if err != nil {
			return err
		}
		if prior {
			return nil
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "propose", Target: "meter_review", ID: out.ID, After: in, RequestID: in.RequestID})
	})
	return out, err
}

// Decide 锁定任务后执行二签，批准时复查首签人的当前权限，审核和审计同事务。
// 驳回不要求首签仍有效，允许重新提交。
func (s MeterReviewService) Decide(ctx context.Context, audit ops.AuditContext, id uint64, in MeterDecisionInput) error {
	if !s.permitted(audit) {
		return operations.ErrForbidden
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.ReviewID == 0 || in.Approve == nil || (!*in.Approve && !operations.ValidText(in.Reason, 500)) {
		return ErrBillingConflict
	}
	return s.Orders.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := LockMeterReviewJob(tx, id); err != nil {
			return err
		}
		review, err := FindMeterReview(ctx, tx, in.ReviewID, id)
		if err != nil {
			return err
		}
		if *in.Approve {
			if s.LookupReviewer == nil {
				return ErrMeterSignerUnavailable
			}
			first, err := s.LookupReviewer(ctx, review.FirstReviewerID)
			if err != nil || first.Role != "customer_finance" || !first.Permitted {
				return ErrMeterSignerUnavailable
			}
		}
		if err := (BillingOrders{DB: tx}).ReviewMeter(ctx, id, in.ReviewID, audit.Actor.ID, *in.Approve, in.Reason); err != nil {
			return err
		}
		if review.SecondReviewerID != nil && *review.SecondReviewerID == audit.Actor.ID {
			return nil
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "decide", Target: "meter_review", ID: in.ReviewID, Before: review, After: in})
	})
}

// ResolveAmount 登记人工金额提案，操作人取当前授权身份；金额、重新排队和首次审计同事务。
func (s MeterReviewService) ResolveAmount(ctx context.Context, audit ops.AuditContext, id uint64, in ManualSettlement) error {
	if !s.permitted(audit) {
		return operations.ErrForbidden
	}
	in.ChargeOrderID = id
	in.ActorID = audit.Actor.ID
	in.Reason = strings.TrimSpace(in.Reason)
	return s.Orders.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := LockMeterReviewJob(tx, id); err != nil {
			return err
		}
		prior, err := ManualAmountExists(tx, id, in.RequestID)
		if err != nil {
			return err
		}
		if err := (BillingOrders{DB: tx}).ResolveAmount(ctx, in); err != nil {
			return err
		}
		if prior {
			return nil
		}
		return ops.AppendAuditInTx(tx, audit, ops.AuditChange{Action: "resolve_amount", Target: "manual_settlement", ID: id, After: in, RequestID: in.RequestID})
	})
}
