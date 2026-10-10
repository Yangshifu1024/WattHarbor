package settlement

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrInvoiceConflict 表示开票审核的状态、签名或内容冲突。
var ErrInvoiceConflict = errors.New("invoice review conflict")

// ReviewerStatus 保存查询时审核人的角色和权限，由后台账号用例提供。
// 第二次审核时重新查询首位审核人的角色和权限。
type ReviewerStatus struct {
	Role      string // 当前角色，第二次审核要求 customer_finance
	Permitted bool   // 当前是否仍有发票复核权限
}

// InvoiceReviewRepo 在调用方事务内执行发票双人复核，由用例同时保存审计。
type InvoiceReviewRepo struct {
	DB *gorm.DB // 结算数据库连接；写方法使用用例传入的事务
	// LookupReviewer 查询首位审核人的当前角色和权限，不使用审核时保存的角色；nil 时拒绝第二次审核。
	LookupReviewer func(ctx context.Context, reviewerID uint64) (ReviewerStatus, error)
}

// InvoiceDecision 是一笔开票审核决定；Approve=true 走双人通过，false 为单人驳回。
type InvoiceDecision struct {
	Approve    bool   // true 为双人通过流程，false 为单人驳回
	InvoiceURL string // 通过时的发票链接，第二次审核须与第一次提交的一致
	Reason     string // 驳回原因，仅驳回时使用
	ActorID    uint64 // 当前审核管理员 ID，由用例实时授权
}

// Review 锁定发票申请后执行审核，返回决定和审核前快照供审计使用。
// 通过须经两名不同且仍有权限的人审核，两次提交的发票链接须一致；驳回只需一人。
// 状态或审核条件不符返回 ErrInvoiceConflict。
func (s InvoiceReviewRepo) Review(ctx context.Context, tx *gorm.DB, id uint64, in InvoiceDecision) (InvoiceAdminReviewStatus, map[string]any, error) {
	status := InvoiceAdminRejected
	// 申请单本体：只用到审核状态和已有的发票链接。
	var invoice struct {
		ReviewStatus InvoiceRequestReviewStatus // 申请单当前审核状态，必须是 pending 才允许本次操作。
		InvoiceURL   *string                    // 申请单上已有的发票链接，可空。
	}
	if err := tx.WithContext(ctx).Table("invoice_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&invoice).Error; err != nil {
		return status, nil, err
	}
	before := map[string]any{"review_status": invoice.ReviewStatus, "invoice_url": invoice.InvoiceURL}
	// 双审进度表：记录是否已有人审、审的是谁、发票链接是什么。
	var review struct {
		ReviewStatus     InvoiceAdminReviewStatus // awaiting_second 表示已首审待复核；空表示还没有审核记录。
		FirstReviewerID  *uint64                  // 首审人；nil 表示还没人首审。
		SecondReviewerID *uint64                  // 二审人；nil 表示尚未复核。
		InvoiceURL       *string                  // 首审人登记的发票链接，二审必须原样提交。
	}
	err := tx.Table("invoice_admin_review").Where("invoice_request_id=?", id).Take(&review).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return status, before, err
	}
	if invoice.ReviewStatus != InvoiceRequestPending {
		return status, before, ErrInvoiceConflict
	}
	if !in.Approve {
		// 取不到审核记录时 ReviewStatus 保持零值（空串），它不是该列的合法取值，
		// 只表示尚未有任何审核记录；此处不替换为 null 也不补 pending。
		if review.ReviewStatus != "" && review.ReviewStatus != InvoiceAdminAwaitingSecond {
			return status, before, ErrInvoiceConflict
		}
		// ON DUPLICATE KEY UPDATE 子句内的 'rejected' 是 invoice_admin_review.review_status
		// 的驳回取值（词表 InvoiceAdminReviewStatus）；与占位符同处一条 SQL，保留字面量。
		if err := tx.Exec("INSERT INTO invoice_admin_review(invoice_request_id,review_status,reject_reason) VALUES (?,'rejected',?) ON DUPLICATE KEY UPDATE review_status='rejected',reject_reason=?,invoice_url=NULL", id, in.Reason, in.Reason).Error; err != nil {
			return status, before, err
		}
		return status, before, tx.Table("invoice_request").Where("id=?", id).Updates(map[string]any{"review_status": InvoiceRequestRejected, "reviewed_by": in.ActorID, "reviewed_at": time.Now().UTC(), "reject_reason": in.Reason}).Error
	}
	if review.FirstReviewerID == nil {
		status = InvoiceAdminAwaitingSecond
		return status, before, tx.Table("invoice_admin_review").Create(map[string]any{"invoice_request_id": id, "review_status": status, "first_reviewer_id": in.ActorID, "invoice_url": in.InvoiceURL}).Error
	}
	if *review.FirstReviewerID == in.ActorID || review.ReviewStatus != InvoiceAdminAwaitingSecond || review.InvoiceURL == nil || *review.InvoiceURL != in.InvoiceURL {
		return status, before, ErrInvoiceConflict
	}
	if s.LookupReviewer == nil {
		return status, before, ErrInvoiceConflict
	}
	first, err := s.LookupReviewer(ctx, *review.FirstReviewerID)
	if err != nil || first.Role != "customer_finance" || !first.Permitted {
		return status, before, ErrInvoiceConflict
	}
	status = InvoiceAdminApproved
	if err := tx.Table("invoice_admin_review").Where("invoice_request_id=?", id).Updates(map[string]any{"review_status": status, "second_reviewer_id": in.ActorID}).Error; err != nil {
		return status, before, err
	}
	return status, before, tx.Table("invoice_request").Where("id=?", id).Updates(map[string]any{"review_status": InvoiceRequestIssued, "invoice_url": in.InvoiceURL, "reviewed_by": in.ActorID, "reviewed_at": time.Now().UTC()}).Error
}
