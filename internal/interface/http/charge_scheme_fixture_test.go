package http

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
)

// Test fixtures freeze the complete scheme and exactly one configured package.
// payment.IntentInput 已随迁 payment 包，本夹具留在 charge 供跨家族集成测试复用。
func chargeCompleteIntent(in payment.IntentInput, cents int64) payment.IntentInput {
	s := charging.Scheme{Name: "集成方案", Amount: &charging.AmountMode{Algorithm: charging.ModeServerEnergy, Periods: []charging.Period{{EndMinute: 1440, ElectricCents: 100, ServiceCents: 40}}}, Packages: []charging.Package{{ID: 1, Name: "金额", Mode: "amount", PriceCents: cents}}}.Normalized()
	in.Rule = charging.Rule{ID: 3, StationID: in.Port.StationID, Version: 1, Spec: s.SpecFor(s.Packages[0])}
	offer := s.Offers(in.Rule)[0]
	in.Offer = &offer
	return in
}

// chargeScanSession 与 payment 包 scan_test.go 内的同名助手保持一致：
// 会话存在、用户有效，供需要鉴权的跨家族集成测试构造会话。
type chargeScanSession struct{}

func (chargeScanSession) Exists(context.Context, string) (bool, error) { return true, nil }
