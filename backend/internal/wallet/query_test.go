package wallet

import "testing"

// TestRefundViewUsesStoredApproval 防止接口丢弃审核结论，把已批准的在途退款误标为拒绝。
func TestRefundViewUsesStoredApproval(t *testing.T) {
	row := WalletRefundRequestRow{RequestID: "request", AmountCents: 100}
	for _, tc := range []struct {
		name     string                // 验证的渠道及审核组合
		parts    []WalletRefundPartRow // 渠道结果
		review   *RefundReviewView     // 审核，可空
		links    int64                 // 风控冻结链接数
		want     string                // 应展示的状态
		refunded int64                 // 应展示已确认成功金额，分
	}{
		{"approved_pending", nil, &RefundReviewView{Approved: true, ActorID: 19, Comment: "通过"}, 1, "pending", 0},
		{"approved_processing", []WalletRefundPartRow{{Status: "processing", RefundCents: 100}}, &RefundReviewView{Approved: true}, 1, "processing", 0},
		{"approved_success", []WalletRefundPartRow{{Status: "success", RefundCents: 100}}, &RefundReviewView{Approved: true}, 1, "success", 100},
		{"rejected", nil, &RefundReviewView{Approved: false}, 1, "rejected", 0},
		{"awaiting", nil, nil, 1, "manual_review", 0},
		{"failure_dominates", []WalletRefundPartRow{{Status: "failed", RefundCents: 40}, {Status: "processing", RefundCents: 60}}, nil, 0, "needs_review", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := refundView(row, tc.parts, tc.review, tc.links)
			if out.Status != tc.want || out.RefundedCents != tc.refunded || out.Review != tc.review {
				t.Fatalf("got %+v", out)
			}
		})
	}
}
