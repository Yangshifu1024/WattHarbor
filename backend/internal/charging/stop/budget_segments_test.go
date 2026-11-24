// 验证心跳可构造分段后，预算封顶能使用 measuredMeter 与 BudgetStopAtMeter 判定。
// 复现设备计时与启动回执相差一秒多的输入；没有分段时仍须拒绝估算停机。
package stop

import (
	"errors"
	"testing"
	"time"

	charging "wattharbor/internal/domain/charging"
	"wattharbor/internal/protocol"
)

// budgetHeartbeatFixture 构造 1500 W、每 15 秒一次的心跳。
// 接收时间比设备计时晚一秒，中央起充记录晚 1.5 秒，覆盖首条心跳早于起充记录的情况。
func budgetHeartbeatFixture(t *testing.T) (AutoStopOrder, []protocol.Event, charging.Rule, *charging.Offer) {
	t.Helper()
	deviceStart := time.Date(2026, 10, 5, 15, 0, 0, 0, time.UTC)
	order := AutoStopOrder{
		ID: 1, OrderNo: "C-BUDGET-1", UserID: 1, DeviceID: "D-BUDGET", PortNo: 1,
		StartedAt: deviceStart.Add(1500 * time.Millisecond), // ack_at 晚于设备自报起点
	}
	rule := charging.Rule{ID: 1, Version: 1, Spec: charging.Spec{
		Mode: charging.ModeServerMaxPower,
		Electric: &charging.ElectricLine{Basis: charging.BasisMaxPower, Periods: []charging.Period{{
			EndMinute: 1440, Tiers: []charging.Tier{
				{MaxWatts: 200, ElectricCents: 50, ServiceCents: 10},
				{MaxWatts: 1000, ElectricCents: 100, ServiceCents: 20},
				{MaxWatts: 3500, ElectricCents: 200, ServiceCents: 30},
			},
		}}},
	}}
	offer := &charging.Offer{ID: 1, StationID: 1, Name: "预算封顶", Mode: "amount", PriceCents: 100}

	// 30 分钟、每 15 秒一条，共 120 条；累计电量按 1500 W 线性更新到 Wh 精度。
	const readings = 120
	samples := make([]protocol.Event, 0, readings)
	for i := 0; i < readings; i++ {
		seconds := uint32(i * 15)
		wh := uint32(1500) * seconds / 3600 // 1500 W 折算 Wh
		samples = append(samples, protocol.Event{
			Type: protocol.Heartbeat, DeviceID: "D-BUDGET",
			ReceivedAt: deviceStart.Add(time.Duration(seconds)*time.Second + time.Second),
			ChargingPorts: []protocol.PortTelemetry{{
				Port: 1, ChargedSeconds: seconds, ChargedMWh: wh * 1000, PowerDeciWatts: 15000,
			}},
		})
	}
	return order, samples, rule, offer
}

func TestBudgetStopReachesDecisionWithSampledSegments(t *testing.T) {
	order, samples, rule, offer := budgetHeartbeatFixture(t)
	latest, ok := latestMeter(samples, order.PortNo, samples[len(samples)-1].ReceivedAt)
	if !ok {
		t.Fatal("夹具应能取到最新计量读数")
	}
	meter := measuredMeter(order, latest, samples)

	// 起充时间相差 1.5 秒仍须生成计量分段，供费用上限判断使用。
	if len(meter.Segments) == 0 {
		t.Fatal("心跳序列应能构造计量分段；为空说明起充时刻容差又收紧了")
	}

	plan, frozen, err := charging.BudgetStopAtMeter(rule, offer, meter)
	if err != nil {
		t.Fatalf("有分段时封顶判定不应报错（修复前恒为 ErrMeterReview）：%v", err)
	}
	if plan.AccruedCents <= 0 {
		t.Fatalf("已充 %d 分钟电，累计费用应为正数，得到 %d 分", meter.ChargedSeconds/60, plan.AccruedCents)
	}
	// 把预算压到 1 分，强制进入封顶分支，这样断言不依赖费率表的绝对数值。
	offer.PriceCents = 1
	plan, frozen, err = charging.BudgetStopAtMeter(rule, offer, meter)
	if err != nil || !plan.ShouldStop {
		t.Fatalf("预算已被超出时应当判停：plan=%+v err=%v", plan, err)
	}
	// 结算侧 readOriginalBillingSource 要求冻结费用严格等于套餐价。
	// 分项合计不符时返回 ErrBillingConflict。
	if frozen == nil {
		t.Fatal("封顶时应冻结费用，供结算直接采用而不重算")
	}
	if frozen.ElectricCents+frozen.ServiceCents != offer.PriceCents || frozen.TotalCents != offer.PriceCents {
		t.Fatalf("冻结费用应精确等于套餐价 %d 分，实际 electric=%d service=%d total=%d",
			offer.PriceCents, frozen.ElectricCents, frozen.ServiceCents, frozen.TotalCents)
	}
	if frozen.ElectricCents < 0 || frozen.ServiceCents < 0 {
		t.Fatalf("冻结费用出现负值分量：%+v", frozen)
	}
}

func TestBudgetStopStillRejectsEvidenceWithoutSegments(t *testing.T) {
	order, samples, rule, offer := budgetHeartbeatFixture(t)
	latest, _ := latestMeter(samples, order.PortNo, samples[len(samples)-1].ReceivedAt)
	meter := measuredMeter(order, latest, samples)
	// 清除分段后无法计算功率费用，须返回计量核实错误。
	meter.Segments = nil
	if _, _, err := charging.BudgetStopAtMeter(rule, offer, meter); !errors.Is(err, charging.ErrMeterReview) {
		t.Fatalf("无分段时仍应要求计量复核，得到 %v", err)
	}
}
