package settlement

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"chargepilot/internal/persist"
	"context"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// RefundAdminService 保存人工退款申请、双人审核和重试请求，不调用外部退款渠道。
type RefundAdminService struct {
	DB         *gorm.DB         // central 资金及审计连接，由进程管理
	ReviewRepo RefundReviewRepo // 退款状态写入和首位审核人权限查询，由 cmd 装配
}

// List 分页读取全局退款记录和执行进度；调用方须先检查 finance.refund.read 权限。
func (s RefundAdminService) List(ctx context.Context, q RefundListQuery) (persist.Page[map[string]any], error) {
	return ListRefunds(ctx, s.DB, q)
}

// ManualRefundRequest 是公开提交内容，也是首次审计快照。
type ManualRefundRequest struct {
	RequestID   string `json:"request_id"`   // 客户端 UUID，首次请求重放沿用
	AmountCents int64  `json:"amount_cents"` // 申请退款额，分，必须为正
	Reason      string `json:"reason"`       // 退款原因，1–255 字
}

// RefundReviewInput 是审核／驳回意见，不保存操作人的会话凭据。
type RefundReviewInput struct {
	ApproveComment string `json:"approve_comment"` // 审核通过的意见
	Reason         string `json:"reason"`          // 驳回的理由
}

// RefundRetryInput 保存退款重试原因，供审计使用。
type RefundRetryInput struct {
	Reason string `json:"reason"` /* 重试原因，1–255 字 */
}

// Request 在支付锁内登记人工退款及首次审计，相同请求返回首次退款号和 created=false。
// 内容不同或金额超限时拒绝，任何写入失败整体回滚；登记不代表渠道已退款。
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

// Review 按支付→退款加锁，核对申请快照和首位审核人的当前权限，审核与审计同事务。
// 批准不表示渠道到账。
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

// Retry 将 automatic 且 pending／processing 的退款重新排队，并同时保存审计。
// 不能绕过人工审批，也不修改金额、单号或渠道结果。
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
