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
	ReviewRepo InvoiceReviewRepo // 发票状态与实时首签权限能力，由 cmd 装配
}

// List 在ctx下读取原后台发票申请及双审进度投影，返回原数组或读取／取消错误。
// 沿用finance.read的全局只读列表，不执行审核，不把列表内容作为当前写入授权。
func (s InvoiceReviewService) List(ctx context.Context) ([]map[string]any, error) {
	return ListInvoiceRequests(ctx, s.DB)
}

// InvoiceReviewInput 保持原公开审核内容，第二签必须核对原链接。
type InvoiceReviewInput struct {
	InvoiceURL string `json:"invoice_url"` // HTTPS 发票链接，审核通过时必填
	Reason     string `json:"reason"`      // 驳回理由，1–255 字
}

// Review 在 ctx 下对 id 执行 approve／驳回，audit 提供实时身份，in 为原提交内容。
// 返回审核状态；不同人员、链接快照和首签当前权限由 repo 复核，失败与审计一起回滚。
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
