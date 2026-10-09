package settlement

import (
	"testing"

	"chargepilot/internal/domain/charging"
)

// TestPriceSourceRejectsUnauditedFinalFee 验证人工核定金额必须有已保存的复核单和有效档位。
// 电费、服务费须非负，合计须等于两项之和且不得超过档位标价。
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
		name string
		// mutate 修改基准输入，构造非法费用边界。
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

// 实际计量计价由 domain/charging 测试覆盖；本测试只检查人工金额是否合法。

// TestPriceSourceAcceptsBoundaryAmounts 验证电费或服务费为零、合计等于档位标价时仍合法。
func TestPriceSourceAcceptsBoundaryAmounts(t *testing.T) {
	offer := &charging.Offer{ID: 1, PriceCents: 1000}
	cases := []struct {
		name string
		// fee 保存核定电费、服务费和合计，单位均为分。
		fee charging.Fee
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
	// 超过档位标价一分也必须拒绝。
	f := charging.Fee{ElectricCents: 400, ServiceCents: 601, TotalCents: 1001}
	s := Source{FinalFee: &f, ReviewID: "RV-1", Offer: offer}
	if _, err := PriceSource(s); err == nil {
		t.Fatal("合计超档位标价一分应被拒绝")
	}
}
