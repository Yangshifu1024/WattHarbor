package settlement

import (
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
)

// TestPriceSourceRejectsUnauditedFinalFee 钉住人工核定金额的准入条件。
//
// 计量复核（跨费率边界没有可用读数）会走 FinalFee 这条路：不再按实际计量算钱，
// 改用复核单上核定的金额。这条路一旦放开就是"人为指定收多少钱"，因此必须同时
// 满足：已持久化且有复核单号、档位存在、两项非负、合计等于两项之和、合计不超过
// 档位标价。缺任何一条都必须拒绝。
//
// 这段判断原先是六项一串 `||`，无法单独验证；PriceSource 是纯函数，抽出后
// 每一条都能钉住。它决定最终向用户收多少钱，错了就是资损。
func TestPriceSourceRejectsUnauditedFinalFee(t *testing.T) {
	offer := &charging.Offer{ID: 1, PriceCents: 1000}
	fee := charging.Fee{ElectricCents: 300, ServiceCents: 100, TotalCents: 400}
	valid := Source{FinalFee: &fee, ReviewID: "RV-1", Offer: offer}

	if got, err := PriceSource(valid); err != nil {
		t.Fatalf("合规的核定金额应被接受，实际 %v", err)
	} else if got.TotalCents != 400 {
		t.Fatalf("应原样返回核定金额，实际 %+v", got)
	}

	cases := []struct {
		name   string
		mutate func(*Source)
	}{
		{"无复核单号", func(s *Source) { s.ReviewID = "" }},
		{"档位缺失", func(s *Source) { s.Offer = nil }},
		{"电费为负", func(s *Source) { f := *s.FinalFee; f.ElectricCents = -1; s.FinalFee = &f }},
		{"服务费为负", func(s *Source) { f := *s.FinalFee; f.ServiceCents = -1; s.FinalFee = &f }},
		{"合计不等于两项之和", func(s *Source) { f := *s.FinalFee; f.TotalCents = 999; s.FinalFee = &f }},
		{"合计超过档位标价", func(s *Source) {
			f := *s.FinalFee
			f.ElectricCents = 900
			f.ServiceCents = 900
			f.TotalCents = 1800
			s.FinalFee = &f
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := valid
			tc.mutate(&s)
			if _, err := PriceSource(s); err == nil {
				t.Fatal("期望被拒绝，实际接受")
			}
		})
	}
}

// 计费回退路径（无核定金额时按实际计量计价）没有在这里测：它需要一份有效的
// charging.Spec.Scheme 才能得出金额，凭空构造的 Spec 会被 SettleSession 正确地
// 以 ErrInvalidPricing 拒绝。写一个"随便造点数据"的用例只会教人这里能糊弄过去，
// 宁可不写。计量计价本身由 domain/charging 的测试覆盖。

// TestPriceSourceAcceptsBoundaryAmounts 确认边界值可用：电费或服务费为 0
// 合法（只有电费的站、只有服务费的站都存在），合计恰好等于档位标价也合法。
func TestPriceSourceAcceptsBoundaryAmounts(t *testing.T) {
	offer := &charging.Offer{ID: 1, PriceCents: 1000}
	cases := []struct {
		name string
		fee  charging.Fee
	}{
		{"只有电费", charging.Fee{ElectricCents: 1000, TotalCents: 1000}},
		{"只有服务费", charging.Fee{ServiceCents: 1000, TotalCents: 1000}},
		{"两项皆零", charging.Fee{}},
		{"恰好等于档位标价", charging.Fee{ElectricCents: 400, ServiceCents: 600, TotalCents: 1000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fee
			s := Source{FinalFee: &f, ReviewID: "RV-1", Offer: offer}
			if _, err := PriceSource(s); err != nil {
				t.Fatalf("边界值应被接受，实际 %v", err)
			}
		})
	}
	// 超过标价一分的必须拒绝——多收一分钱也是错的。
	f := charging.Fee{ElectricCents: 400, ServiceCents: 601, TotalCents: 1001}
	s := Source{FinalFee: &f, ReviewID: "RV-1", Offer: offer}
	if _, err := PriceSource(s); err == nil {
		t.Fatal("合计超档位标价一分应被拒绝")
	}
}
