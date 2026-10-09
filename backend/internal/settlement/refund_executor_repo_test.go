package settlement

import (
	"testing"

	"chargepilot/internal/payment"
)

// TestReceiptMatchesRejectsAnyMismatch 验证渠道回执任一身份或金额字段不匹配时都不能入账。
func TestReceiptMatchesRejectsAnyMismatch(t *testing.T) {
	base := payment.RefundRequest{
		RefundNo: "R-1", MerchantOrderNo: "P-1", TransactionID: "T-1",
		TotalCents: 1000, RefundCents: 100,
	}
	ok := payment.RefundResult{
		RefundNo: "R-1", MerchantOrderNo: "P-1", TransactionID: "T-1",
		TotalCents: 1000, RefundCents: 100, RefundID: "wx-1", Status: "success",
	}
	if !receiptMatches(base, ok) {
		t.Fatal("完全一致的回执应被接受")
	}

	for name, mutate := range map[string]func(*payment.RefundResult){
		"退款单号不符":   func(r *payment.RefundResult) { r.RefundNo = "R-2" },
		"商户单号不符":   func(r *payment.RefundResult) { r.MerchantOrderNo = "P-2" },
		"渠道流水号不符":  func(r *payment.RefundResult) { r.TransactionID = "T-2" },
		"原单总额不符":   func(r *payment.RefundResult) { r.TotalCents = 2000 },
		"退款金额不符":   func(r *payment.RefundResult) { r.RefundCents = 200 },
		"渠道退款单号为空": func(r *payment.RefundResult) { r.RefundID = "" },
		"渠道退款单号超长": func(r *payment.RefundResult) { r.RefundID = longStr(65) },
	} {
		r := ok
		mutate(&r)
		if receiptMatches(base, r) {
			t.Errorf("%s：回执与请求不符却��接受", name)
		}
	}
	// 边界：渠道退款单号恰好 64 位应接受，与其它字段的 64 上限一致。
	edge := ok
	edge.RefundID = longStr(64)
	if !receiptMatches(base, edge) {
		t.Error("渠道退款单号恰好 64 位应接受")
	}
}

// TestProviderMatchesChannel 验证模拟交易和真实渠道的两种错配都被拒绝。
func TestProviderMatchesChannel(t *testing.T) {
	cases := []struct {
		name string
		// provider 是退款渠道名称。
		provider string
		// transaction 是交易号；SIM 前缀表示模拟，空串用于非模拟边界。
		transaction string
		want        bool
	}{
		{"模拟器结算模拟交易", "simulation", "SIM20260101", true},
		{"真实渠道结算真实交易", "wechat", "4200001234", true},
		{"模拟器结算真实交易", "simulation", "4200001234", false},
		{"真实渠道结算模拟交易", "wechat", "SIM20260101", false},
		{"交易号为空视为非模拟", "wechat", "", true},
		{"交易号 SIM 开头的边界", "simulation", "SIM", true},
		{"仅以 SIM 开头才算模拟", "simulation", "XSIM20260101", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerMatchesChannel(tc.provider, tc.transaction); got != tc.want {
				t.Fatalf("期望 %v，实际 %v", tc.want, got)
			}
		})
	}
}

// longStr 构造 n 个 ASCII x；n 为负时由 make 触发 panic。
func longStr(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
