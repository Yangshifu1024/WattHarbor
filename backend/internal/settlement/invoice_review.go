package settlement

import (
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/ops"
	"context"
	"gorm.io/gorm"
)

// InvoiceReviewService 拥有开票双审、拒绝及审计的完整事务。
type InvoiceReviewService struct {
	DB         *gorm.DB          // central 发票及审计连接，由进程管理
	ReviewRepo InvoiceReviewRepo // 发票状态写入和首位审核人权限查询，由 cmd 装配
}

// List 读取全局发票申请和双审进度；调用方须先检查 finance.read 权限。
func (s InvoiceReviewService) List(ctx context.Context) ([]map[string]any, error) {
	return ListInvoiceRequests(ctx, s.DB)
}

// InvoiceReviewInput 保存审核意见，两次通过审核须提交相同的发票链接。
type InvoiceReviewInput struct {
	InvoiceURL string `json:"invoice_url"` // HTTPS 发票链接，审核通过时必填
	Reason     string `json:"reason"`      // 驳回理由，1–255 字
}

// Review 执行发票通过或驳回，核对审核人、发票链接和首位审核人的当前权限；审核与审计一起提交或回滚。
func (s InvoiceReviewService) Review(ctx context.Context, audit ops.AuditContext, id uint64, approve bool, in InvoiceReviewInput) (string, error) {
	if audit.Actor.Role != "customer_finance" || !audit.Actor.HasPermission("invoice.review") {
		return "", operations.ErrForbidden
	}
	if (approve && !operations.ValidPublicURL(in.InvoiceURL)) || (!approve && !operations.ValidText(in.Reason, 255)) {
		return "", operations.ErrConflict
	}
	status := ""
	var before map[string]any
	var changes []ops.AuditChange
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		status, before, err = s.ReviewRepo.Review(ctx, tx, id, InvoiceDecision{
			Approve: approve, InvoiceURL: in.InvoiceURL, Reason: in.Reason, ActorID: audit.Actor.ID,
		})
		if err != nil {
			return err
		}
		changes = []ops.AuditChange{{Action: status, Target: "invoice", ID: id, Before: before, After: in, RequestID: ""}}
		for _, change := range changes {
			if err := ops.AppendAuditInTx(tx, audit, change); err != nil {
				return err
			}
		}
		return nil
	})
	return status, err
}
