package wallet

import (
	"context"
	"errors"
	"time"

	"chargepilot/internal/persist"
	"gorm.io/gorm"
)

// QueryService 查询当前用户的余额、充值付款条件和退款进度。
type QueryService struct {
	Repo UserWalletRepo // 仅查询调用方用户的数据，由 cmd 提供连接
}

// BalanceView 是账户展示值，金额均以分为单位。
type BalanceView struct {
	BalanceCents   int64  `json:"balance_cents"`   // 账面余额
	FrozenCents    int64  `json:"frozen_cents"`    // 退款预占等已冻结金额
	AvailableCents int64  `json:"available_cents"` // 余额减冻结额，下限为零
	Status         string `json:"status"`          // active 或 frozen；未开户显示 active
}

// Balance 读取 userID 的余额；未开户返回可用的零余额视图。
// 返回存储或取消错误，不创建账户，不以流水重算余额。
func (s QueryService) Balance(ctx context.Context, userID uint64) (BalanceView, error) {
	w, err := s.Repo.FindWalletBalance(ctx, userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return BalanceView{Status: "active"}, nil
	}
	if err != nil {
		return BalanceView{}, err
	}
	return BalanceView{w.BalanceCents, w.FrozenCents, w.AvailableCents(), w.Status}, nil
}

// Transactions 按用户、分页与 direction 查询流水；空方向查全部，in、out 分别查收入、支出。
func (s QueryService) Transactions(ctx context.Context, userID uint64, direction string, page, size int) (persist.Page[map[string]any], error) {
	return s.Repo.ListWalletTxns(ctx, userID, direction, page, size)
}

// Recharges 分页读取 userID 的申请，并以 now 判定未支付且尚未过期的续付资格。
// 返回页数据；不延长有效期，不创建新的支付单。
func (s QueryService) Recharges(ctx context.Context, userID uint64, page, size int, now time.Time) (persist.Page[WalletRechargeRow], error) {
	out, err := s.Repo.ListWalletRecharges(ctx, userID, page, size)
	if err != nil {
		return out, err
	}
	for i := range out.Items {
		r := &out.Items[i]
		r.CanPay = r.Status == "initiated" && r.ExpiredAt != nil && r.ExpiredAt.After(now)
	}
	return out, nil
}

// RefundReviewView 定义审核展示字段，包括审核人数字 ID、结论与依据。
type RefundReviewView struct {
	Approved bool   `json:"approved"` // 实际保存的通过或拒绝结论
	Comment  string `json:"comment"`  // 审核依据
	ActorID  uint64 `json:"actor_id"` // 审核人
}

// RefundView 是一笔申请及原路退款的进度，不把批准审核等同于退款完成。
type RefundView struct {
	RequestID     string                `json:"request_id"`       // 用户申请 UUID
	AmountCents   int64                 `json:"amount_cents"`     // 申请金额，分
	RefundedCents int64                 `json:"refunded_cents"`   // 已确认成功的金额，分
	Status        string                `json:"status"`           // pending、processing、needs_review、success、manual_review 或 rejected
	Reason        *string               `json:"reason"`           // 申请依据，可空
	CreatedAt     time.Time             `json:"created_at"`       // 申请创建时间
	RefundOrders  []WalletRefundPartRow `json:"refund_orders"`    // 各来源退款及渠道结果，空时为数组
	Review        *RefundReviewView     `json:"review,omitempty"` // 未审核时省略
}

// Refunds 分页读取 userID 的申请，一次批量补齐本页退款、审核及冻结链接。
// 返回稳定分页结构；每次查询至多五条 SQL，不随本页记录数增长，不执行资金写入。
func (s QueryService) Refunds(ctx context.Context, userID uint64, page, size int) (persist.Page[RefundView], error) {
	rows, err := s.Repo.ListWalletRefundRequests(ctx, userID, page, size)
	out := persist.Page[RefundView]{Items: []RefundView{}, Total: rows.Total, Page: page, PageSize: size}
	if err != nil {
		return out, err
	}
	ids := make([]string, len(rows.Items))
	for i, row := range rows.Items {
		ids[i] = row.RequestID
	}
	parts, reviews, links, err := s.Repo.RefundDetails(ctx, ids)
	if err != nil {
		return out, err
	}
	for _, row := range rows.Items {
		p := parts[row.RequestID]
		if p == nil {
			p = []WalletRefundPartRow{}
		}
		out.Items = append(out.Items, refundView(row, p, reviews[row.RequestID], links[row.RequestID]))
	}
	return out, nil
}

// refundView 组合退款分笔、审核与冻结记录的展示结果。
// 退款成功以渠道确认金额为准；未审核且有关联冻结时显示待人工审核，明确拒绝后才显示 rejected。
func refundView(row WalletRefundRequestRow, parts []WalletRefundPartRow, review *RefundReviewView, linked int64) RefundView {
	out := RefundView{RequestID: row.RequestID, AmountCents: row.AmountCents, Reason: row.Reason, CreatedAt: row.CreatedAt, Status: "pending", RefundOrders: parts, Review: review}
	for _, part := range parts {
		switch part.Status {
		case "success":
			out.RefundedCents += part.RefundCents
		case "failed", "needs_review":
			out.Status = "needs_review"
		case "processing":
			if out.Status != "needs_review" {
				out.Status = "processing"
			}
		}
	}
	if out.RefundedCents == row.AmountCents {
		out.Status = "success"
	}
	if linked > 0 && review == nil {
		out.Status = "manual_review"
	} else if review != nil && !review.Approved {
		out.Status = "rejected"
	}
	return out
}
