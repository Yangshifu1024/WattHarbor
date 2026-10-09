package settlement

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/persist"
	"context"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// RefundAdminService 拥有人工退款申请、双人复核与重投事务，不请求外部渠道。
type RefundAdminService struct {
	DB         *gorm.DB         // central 资金及审计连接，由进程管理
	ReviewRepo RefundReviewRepo // 退款状态能力及实时首签人查询，由 cmd 装配
}

// List 在ctx下按原q分页读取后台退款及执行进度，返回原页或读取／取消错误。
// 不执行双审或重投，不改变finance.refund.read的全局列表边界和原可空字段。
func (s RefundAdminService) List(ctx context.Context, q RefundListQuery) (persist.Page[map[string]any], error) {
	return ListRefunds(ctx, s.DB, q)
}

// ManualRefundRequest 是公开提交内容，也是首次审计快照。
type ManualRefundRequest struct {
	RequestID   string `json:"request_id"`   // 客户端 UUID，原请求重放沿用
	AmountCents int64  `json:"amount_cents"` // 申请退款额，分，必须为正
	Reason      string `json:"reason"`       // 退款原因，1–255 字
}

// RefundReviewInput 是审核／驳回意见，不保存操作人的会话凭据。
type RefundReviewInput struct {
	ApproveComment string `json:"approve_comment"` // 审核通过的原意见
	Reason         string `json:"reason"`          // 驳回的原理由
}

// RefundRetryInput 是重投审计依据，不改变退款金额或状态。
type RefundRetryInput struct {
	Reason string `json:"reason"` /* 重投原因，1–255 字 */
}

// Request 在 ctx 下为 orderID 提交 in 人工退款，audit 为实时授权和审计事实。
// 返回首次创建标志、原退款单号及错误；相同请求返回 false 和原单号，异内容或额度冲突拒绝。
// 支付锁内的申请及首次审计原子提交；权限不足返回禁止，存储／审计失败整体回滚，不请求渠道。
func (s RefundAdminService) Request(ctx context.Context, audit ops.AuditContext, orderID uint64, in ManualRefundRequest) (bool, string, error) {
	if !audit.Actor.HasPermission("order.refund.create") {
		return false, "", operations.ErrForbidden
	}
	if uuid.Validate(in.RequestID) != nil || in.AmountCents <= 0 || !operations.ValidText(in.Reason, 255) {
		return false, "", operations.ErrConflict
	}
	created := false
	refundNo := ""
	var recordID uint64
	var changes []ops.AuditChange
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		created, refundNo, recordID, err = s.ReviewRepo.ManualCreate(ctx, tx, ManualRefundInput{
			ActorID: audit.Actor.ID, OrderID: orderID, RequestID: in.RequestID, AmountCents: in.AmountCents, Reason: in.Reason,
		})
		if err != nil {
			return err
		}
		if created {
			changes = []ops.AuditChange{{Action: "request", Target: "refund", ID: recordID, Before: nil, After: in, RequestID: in.RequestID}}
		}
		for _, change := range changes {
			if err := ops.AppendAuditInTx(tx, audit, change); err != nil {
				return err
			}
		}
		return nil
	})
	return created, refundNo, err
}

// Review 在 ctx 下处理原退款身份及 in 内容，audit 为实时授权和审计事实。
// 返回原申请或审核状态；异内容／状态返回冲突，权限不足返回禁止，存储／审计失败回滚。
// 保留支付→退款锁和实时首签权限核对；审核通过仍不能表示渠道到账。
func (s RefundAdminService) Review(ctx context.Context, audit ops.AuditContext, refundNo string, approve bool, in RefundReviewInput) (string, error) {
	if !audit.Actor.HasPermission("order.refund.review") || audit.Actor.Role != "customer_finance" {
		return "", operations.ErrForbidden
	}
	if (approve && !operations.ValidText(in.ApproveComment, 255)) || (!approve && !operations.ValidText(in.Reason, 255)) {
		return "", operations.ErrConflict
	}
	status := ""
	var recordID uint64
	var changes []ops.AuditChange
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		status, recordID, err = s.ReviewRepo.Review(ctx, tx, refundNo, ReviewDecision{
			Approve: approve, ApproveComment: in.ApproveComment, Reason: in.Reason, ActorID: audit.Actor.ID,
		})
		if err != nil {
			return err
		}
		changes = []ops.AuditChange{{Action: status, Target: "refund", ID: recordID, Before: nil, After: in, RequestID: ""}}
		for _, change := range changes {
			if err := ops.AppendAuditInTx(tx, audit, change); err != nil {
				return err
			}
		}
		return nil
	})
	return status, err
}

// Retry 在 ctx 下按 refundNo 将原退款重排队，in.Reason 是原审计依据，audit 提供实时权限。
// 返回 scheduled 或错误；仅 automatic 且 pending／processing 可重投，人工待审不能绕过审批。
// 排队时刻与审计同事务，权限不足、状态冲突或写入失败不提交；不修改金额、原单号或渠道结果。
func (s RefundAdminService) Retry(ctx context.Context, audit ops.AuditContext, refundNo string, in RefundRetryInput) (string, error) {
	if !audit.Actor.HasPermission("finance.refund.retry") {
		return "", operations.ErrForbidden
	}
	if !operations.ValidText(in.Reason, 255) {
		return "", operations.ErrConflict
	}
	var recordID uint64
	var changes []ops.AuditChange
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		recordID, err = s.ReviewRepo.Retry(ctx, tx, refundNo)
		if err != nil {
			return err
		}
		changes = []ops.AuditChange{{Action: "retry", Target: "refund", ID: recordID, Before: nil, After: in, RequestID: ""}}
		for _, change := range changes {
			if err := ops.AppendAuditInTx(tx, audit, change); err != nil {
				return err
			}
		}
		return nil
	})
	return "scheduled", err
}
