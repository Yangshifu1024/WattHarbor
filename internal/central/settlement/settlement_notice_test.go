package settlement

import (
	"github.com/ChargePilot2026/charge-pilot/internal/central/order"
	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"testing"
)

func TestSettlementNoticeUsesFrozenDisplayAndFinalAmounts(t *testing.T) {
	fee := charging.Fee{ElectricCents: 80, ServiceCents: 40, TotalCents: 120}
	notice := settlementNotice("event", order.ChargeOrderRecord{ID: 1, UserID: 2}, charging.Display{}, fee, 80)
	if _, ok := notice["total_cents"]; ok {
		t.Fatal("hidden end fee leaked into notification")
	}
	notice = settlementNotice("event", order.ChargeOrderRecord{ID: 1, UserID: 2}, charging.Display{ShowFeeOnEnd: true}, fee, 80)
	if notice["total_cents"] != int64(120) || notice["refund_cents"] != int64(80) {
		t.Fatal(notice)
	}
	if _, ok := notice["electric_cents"]; ok {
		t.Fatal("hidden split leaked")
	}
}
