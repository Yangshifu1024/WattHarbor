package settlement

import (
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/payment"
)

// TestReceiptMatchesRejectsAnyMismatch 逐项钉住渠道回执的核对。
//
// 渠道回执是外部输入，任何一项对不上都不能入账：把 A 单的回执当成 B 单的
// 就会按 B 单的金额退钱，钱出去了账没对上。这段判断原本是 apply 里一长串
// `||`，逐项无法单独验证；抽成纯函数后每个字段都能单独钉住。
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

// TestProviderMatchesChannel 钉住"交易与结算它的渠道必须同属真实或模拟一侧"。
//
// 模拟器产生的交易号（SIM 前缀）发到真实商户，真实用户的钱就真退了；
// 真实渠道的交易让模拟器去结算，测试环境会凭空减掉用户余额。
// 两个方向都是资金事故，所以分开钉。
func TestProviderMatchesChannel(t *testing.T) {
	cases := []struct {
		// name 当前边界场景名称，供 t.Run 区分断言；不作为业务身份或数据库条件。
		name string
		// provider 本轮退款渠道名，用于和交易流水号的模拟／真实来源比对。
		provider string
		// transaction 本轮原渠道交易号；SIM 前缀表示模拟来源，空串作为既有非模拟边界测试。
		transaction string
		// want 渠道与交易来源是否应匹配的预期布尔结果。
		want bool
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

// longStr 接收非负字节长度 n，返回由 n 个 ASCII x 组成的字符串；零返回空串，负数由 make 触发 panic，无错误返回或外部 IO。
func longStr(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
