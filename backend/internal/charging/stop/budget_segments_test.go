// 本文件覆盖自动停机的预算封顶路径在计量分段可构造时的行为。
//
// 背景：auto_stop.go 在预算模式下调用 BudgetStopAtMeter，拿到错误时只记一行
// "charge budget unavailable" 日志、不停机。而 budget_stop.go 的兜底分支要求
// len(meter.Segments) > 0 才会退回上下界估算。
//
// MeasuredSegments 修复前，首条心跳常落在 charge_order.started_at 之前一两秒
// （设备按自身时钟先起充、START 回执后到），整段证据被判矛盾而丢弃，
// server_max_power 且电量为正的会话因此拿不到任何分段——预算封顶从未生效过。
// 修复后分段可以构造，封顶路径第一次真正可达。
//
// 这里用真实的 measuredMeter 与 BudgetStopAtMeter 走完整计算路径，
// 不替换中间任何一层。
package stop

import (
	"errors"
	"testing"
	"time"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

// budgetHeartbeatFixture 造一段持续充电的心跳序列：设备 1500 W，每 15 秒一条。
//
// 刻意复现自动停机的真实输入形态——心跳接收时刻比设备自身秒表位置晚 1 秒，
// 而 charge_order.started_at 来自 START 回执 ack_at，比设备自报起点晚 1.5 秒。
// 修复前第一条心跳因此落在 cursor 之前，整段被判矛盾。
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

	// 30 分钟、每 15 秒一条，共 120 条；累计电量按 1500 W 线性推进到 Wh 精度。
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

	// 修复前这一项为 0，整段证据被丢弃、封顶路径不可达。
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
	// 结算侧 readOriginalBillingSource 要求冻结费用严格等于套餐价，
	// 不等会直接判 ErrBillingConflict。这是刚被启用的路径必须守住的不变量。
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
	// 剥掉分段还原修复前的状态：兜底分支要求 len(Segments) > 0，
	// 因此必须仍然拒绝，而不是悄悄放行一个没有证据的停机决策。
	meter.Segments = nil
	if _, _, err := charging.BudgetStopAtMeter(rule, offer, meter); !errors.Is(err, charging.ErrMeterReview) {
		t.Fatalf("无分段时仍应要求计量复核，得到 %v", err)
	}
}
