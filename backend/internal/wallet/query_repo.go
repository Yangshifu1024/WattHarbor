package wallet

import "context"

// RefundDetails 为本页 requestIDs 批量读取退款分笔、审核及冻结链接数。
// 返回以申请 UUID 索引的三个集合；空页不查询，任一读取失败返回错误，不修改资金或审核记录。
// UUID 已由用户所属申请页筛选，补查不接受外部直接提交的任意请求号。
func (s UserWalletRepo) RefundDetails(ctx context.Context, requestIDs []string) (map[string][]WalletRefundPartRow, map[string]*RefundReviewView, map[string]int64, error) {
	parts := map[string][]WalletRefundPartRow{}
	reviews := map[string]*RefundReviewView{}
	links := map[string]int64{}
	if len(requestIDs) == 0 {
		return parts, reviews, links, nil
	}
	var rows []struct {
		RequestID           string // 申请 UUID
		WalletRefundPartRow        // 渠道结果查询结果
	}
	if err := s.DB.WithContext(ctx).Table("wallet_refund_part p").Select("p.request_id,r.refund_no,r.refund_cents,r.status,r.failure_reason,r.completed_at").Joins("JOIN refund_record r ON r.id=p.refund_record_id").Where("p.request_id IN ?", requestIDs).Order("r.id").Find(&rows).Error; err != nil {
		return nil, nil, nil, err
	}
	for _, row := range rows {
		parts[row.RequestID] = append(parts[row.RequestID], row.WalletRefundPartRow)
	}
	var rv []struct {
		RequestID        string // 申请 UUID
		RefundReviewView        // 审核人、结论与依据
	}
	if err := s.DB.WithContext(ctx).Table("wallet_risk_review").Select("request_id,approved,comment,actor_id").Where("request_id IN ?", requestIDs).Find(&rv).Error; err != nil {
		return nil, nil, nil, err
	}
	for _, row := range rv {
		review := row.RefundReviewView
		reviews[row.RequestID] = &review
	}
	var counts []struct {
		RequestID string // 申请 UUID
		N         int64  // 关联冻结数量
	}
	if err := s.DB.WithContext(ctx).Table("wallet_risk_freeze_link").Select("request_id,COUNT(*) AS n").Where("request_id IN ?", requestIDs).Group("request_id").Find(&counts).Error; err != nil {
		return nil, nil, nil, err
	}
	for _, row := range counts {
		links[row.RequestID] = row.N
	}
	return parts, reviews, links, nil
}
