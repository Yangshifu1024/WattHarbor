package charging

import (
	"errors"
	"testing"
	"time"
)

// 本文件逐条枚举 usageFromMeter（actual.go）的全部计量复核触发条件。
//
// 该函数是消费封顶（StopAtMeter）与最终结算（PriceActual）共用的计量校验入口，
// 任一分支放松都会让缺乏证据的电量按估算计价，任一分支收紧都会把真实订单
// 推进人工复核。因此这里不满足于「部分用例间接覆盖」，而是把每个 ErrMeterReview
// 返回点各对应一行用例，正向对照单独成表，锁住两条边界。
//
// 全部用例通过公开入口 PriceActual 断言，不直接调用未导出的 usageFromMeter。

// actualStart 是共享的起充时刻，取北京时间上午整点，避开日界与时段边界，
// 使单时段费率用例不受跨档干扰。
var actualStart = time.Date(2026, 10, 5, 9, 0, 0, 0, beijing)

// energyRule 构造按电量计价的合法规则。电量口径每个时段只有单一电价，
// 不允许携带功率阶梯；periods 决定费率在一天内是否变化。
func energyRule(periods ...Period) Rule {
	return Rule{ID: 7, Version: 1, Spec: Spec{Mode: ModeServerEnergy,
		Electric: &ElectricLine{Basis: BasisEnergy, Periods: periods}}}
}

// tierRule 构造带功率阶梯的服务端规则，mode 只接受两种功率口径，
// 电价口径由模式推导，保证 ValidateSpec 通过。
func tierRule(mode ChargeMode, tiers []Tier) Rule {
	return Rule{ID: 7, Version: 1, Spec: Spec{Mode: mode,
		Electric: &ElectricLine{Basis: mode.BasisFor(), Periods: []Period{{EndMinute: 1440, Tiers: tiers}}}}}
}

// actualTiers 是功率用例共用的两档阶梯：600 W 落第二档，150 W 落第一档。
// 费率取整除小时数后恰好为整数分的值，避免十进制舍入参与断言。
func actualTiers() []Tier {
	return []Tier{
		{MaxWatts: 200, ElectricCents: 100, ServiceCents: 20},
		{MaxWatts: 3500, ElectricCents: 180, ServiceCents: 30},
	}
}

// energyMeter 构造两段首尾相接的合法计量：30 分钟 1 Wh 加 30 分钟 2 Wh，
// 电量口径分段不要求功率，供边界矛盾类用例在其上做单点变异。
func energyMeter() ActualMeter {
	power := uint32(0)
	return ActualMeter{
		StartedAt: actualStart, EndedAt: actualStart.Add(time.Hour),
		ChargedWh: 3, ChargedSeconds: 3600,
		Segments: []MeterSegment{
			{StartedAt: actualStart, EndedAt: actualStart.Add(30 * time.Minute), EnergyWh: 1},
			{StartedAt: actualStart.Add(30 * time.Minute), EndedAt: actualStart.Add(time.Hour), EnergyWh: 2, PeakW: power},
		},
	}
}

// secondSegments 构造 n 个一秒一段、零电量的分段链，恰好收口到 n 秒处，
// 用于分段数上限的边界对照：链本身完全合法，只有条数超限才被拒绝。
func secondSegments(start time.Time, n int) []MeterSegment {
	segments := make([]MeterSegment, n)
	for i := range segments {
		segments[i] = MeterSegment{
			StartedAt: start.Add(time.Duration(i) * time.Second),
			EndedAt:   start.Add(time.Duration(i+1) * time.Second),
		}
	}
	return segments
}

// TestUsageFromMeterReviewTriggers 把 usageFromMeter 的每个 ErrMeterReview
// 返回点各对应一行：预设复核标记、时段四项（零时刻、倒置、跨七天、
// 累计秒数超墙钟加五分钟）、分段数上限、无分段跨费率、分段边界五类矛盾
// （首段错位、段间缺口、时刻倒置、越界、零时长带电量）、两种功率口径的
// 功率缺失，以及收口与合计两项终检。
func TestUsageFromMeterReviewTriggers(t *testing.T) {
	uniform := []Period{{EndMinute: 1440, ElectricCents: 100}}
	// 跨档电量费率：前 10 分钟 100 分/度、之后 200 分/度，用于无分段跨费率用例。
	stepped := []Period{{EndMinute: 10, ElectricCents: 100}, {EndMinute: 1440, ElectricCents: 200}}
	cases := []struct {
		name  string
		rule  Rule
		meter func() ActualMeter
		why   string
	}{
		{name: "预设复核标记直接复核", rule: energyRule(uniform...),
			meter: func() ActualMeter {
				m := ActualMeter{StartedAt: actualStart, EndedAt: actualStart.Add(time.Hour), ChargedWh: 1000, ChargedSeconds: 3600, ReviewRequired: true}
				return m
			},
			why: "上游已判定证据不足时不得再尝试计价"},
		{name: "起充时刻为零", rule: energyRule(uniform...),
			meter: func() ActualMeter { return ActualMeter{EndedAt: actualStart} },
			why:   "没有起充时刻就无法定位费率时段"},
		{name: "结束早于开始", rule: energyRule(uniform...),
			meter: func() ActualMeter { return ActualMeter{StartedAt: actualStart, EndedAt: actualStart.Add(-time.Second)} },
			why:   "时段倒置属证据矛盾"},
		{name: "跨距超过七天", rule: energyRule(uniform...),
			meter: func() ActualMeter {
				return ActualMeter{StartedAt: actualStart, EndedAt: actualStart.Add(7*24*time.Hour + time.Second)}
			},
			why: "超距窗口超出结算允许范围"},
		{name: "累计秒数超出墙钟加五分钟", rule: energyRule(uniform...),
			meter: func() ActualMeter {
				return ActualMeter{StartedAt: actualStart, EndedAt: actualStart.Add(time.Hour), ChargedSeconds: 7200}
			},
			why: "充电秒数超过物理可能的容许量"},
		{name: "分段数超过上限", rule: energyRule(uniform...),
			meter: func() ActualMeter {
				return ActualMeter{StartedAt: actualStart, EndedAt: actualStart.Add(10081 * time.Second), Segments: secondSegments(actualStart, 10081)}
			},
			why: "分段条数上限防止异常设备放大计费计算"},
		{name: "无分段且费率跨档", rule: energyRule(stepped...),
			meter: func() ActualMeter {
				// 窗口取北京时间 00:05–00:15，恰好跨过 10 分钟处的费率边界。
				start := time.Date(2026, 10, 5, 0, 5, 0, 0, beijing)
				return ActualMeter{StartedAt: start, EndedAt: start.Add(10 * time.Minute), ChargedWh: 100, ChargedSeconds: 600}
			},
			why: "跨费率电量必须有边界实测，不按时间均摊"},
		{name: "首段起点错位", rule: energyRule(uniform...),
			meter: func() ActualMeter {
				m := energyMeter()
				m.Segments[0].StartedAt = actualStart.Add(10 * time.Second)
				return m
			},
			why: "首段必须从计量起点开始"},
		{name: "段间出现缺口", rule: energyRule(uniform...),
			meter: func() ActualMeter {
				m := energyMeter()
				m.Segments[1].StartedAt = actualStart.Add(40 * time.Minute)
				return m
			},
			why: "缺口时间的电量没有归属"},
		{name: "段内时刻倒置", rule: energyRule(uniform...),
			meter: func() ActualMeter {
				m := energyMeter()
				m.Segments[1] = MeterSegment{StartedAt: actualStart.Add(30 * time.Minute), EndedAt: actualStart.Add(25 * time.Minute), EnergyWh: 2}
				return m
			},
			why: "段内时刻倒置属证据矛盾"},
		{name: "段终点越过计量终点", rule: energyRule(uniform...),
			meter: func() ActualMeter {
				m := energyMeter()
				m.Segments[1].EndedAt = actualStart.Add(time.Hour + time.Second)
				return m
			},
			why: "分段不得超出计量声明的时间范围"},
		{name: "零时长段携带电量", rule: energyRule(uniform...),
			meter: func() ActualMeter {
				m := energyMeter()
				m.Segments = append(m.Segments[:1], MeterSegment{StartedAt: actualStart.Add(30 * time.Minute), EndedAt: actualStart.Add(30 * time.Minute), EnergyWh: 1})
				return m
			},
			why: "零时长段不可能产生真实电量"},
		{name: "实时功率缺实测读数", rule: tierRule(ModeServerRealtimePower, actualTiers()),
			meter: func() ActualMeter {
				peak := uint32(500)
				return ActualMeter{StartedAt: actualStart, EndedAt: actualStart.Add(time.Hour), ChargedWh: 1, ChargedSeconds: 3600,
					Segments: []MeterSegment{{StartedAt: actualStart, EndedAt: actualStart.Add(time.Hour), EnergyWh: 1, PeakW: peak}}}
			},
			why: "实时功率口径只能用实测功率定价，峰值不能替代"},
		{name: "功率分档零功率且无实测", rule: tierRule(ModeServerMaxPower, actualTiers()),
			meter: func() ActualMeter {
				return ActualMeter{StartedAt: actualStart, EndedAt: actualStart.Add(time.Hour), ChargedWh: 1, ChargedSeconds: 3600,
					Segments: []MeterSegment{{StartedAt: actualStart, EndedAt: actualStart.Add(time.Hour), EnergyWh: 1}}}
			},
			why: "已充电量大于零却没有任何功率证据时不得定价"},
		{name: "分段未收口到终点", rule: energyRule(uniform...),
			meter: func() ActualMeter { m := energyMeter(); m.Segments = m.Segments[:1]; return m },
			why:   "最后一段之后的电量没有归属"},
		{name: "分段电量合计不符", rule: energyRule(uniform...),
			meter: func() ActualMeter { m := energyMeter(); m.Segments[1].EnergyWh = 1; return m },
			why:   "分段求和必须等于声明总量"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PriceActual(tc.rule, tc.meter()); !errors.Is(err, ErrMeterReview) {
				t.Fatalf("应要求计量复核却得到 %v：%s", err, tc.why)
			}
		})
	}
}

// TestPriceActualPositiveControls 与复核触发表逐点对照，锁住不应被收紧的
// 合法路径：整段总量配均匀费率、累计秒数的五分钟容差、峰值功率独立定价、
// 分段数恰好在上限，以及带实测功率的分档计价。费率与时长全部取整除组合，
// 断言精确到分。
func TestPriceActualPositiveControls(t *testing.T) {
	t.Run("整段总量配全天单一电价", func(t *testing.T) {
		fee, err := PriceActual(energyRule(Period{EndMinute: 1440, ElectricCents: 100}),
			ActualMeter{StartedAt: actualStart, EndedAt: actualStart.Add(time.Hour), ChargedWh: 1000, ChargedSeconds: 3600})
		if err != nil {
			t.Fatalf("均匀费率下整段电量可直接结算：%v", err)
		}
		if fee.ElectricCents != 100 || fee.ServiceCents != 0 || fee.TotalCents != 100 || fee.BillableWh != 1000 {
			t.Fatalf("费用 %+v，应为一分/度下 1000 Wh 计 100 分", fee)
		}
	})
	t.Run("累计秒数在墙钟加五分钟内", func(t *testing.T) {
		fee, err := PriceActual(energyRule(Period{EndMinute: 1440, ElectricCents: 100}),
			ActualMeter{StartedAt: actualStart, EndedAt: actualStart.Add(time.Hour), ChargedWh: 1000, ChargedSeconds: 3900})
		if err != nil {
			t.Fatalf("容许量内的累计秒数不应触发复核：%v", err)
		}
		if fee.TotalCents != 100 {
			t.Fatalf("费用 %d 分，应为 100 分", fee.TotalCents)
		}
	})
	t.Run("峰值功率可独立定价", func(t *testing.T) {
		// 功率分档口径下，峰值可验证的段即使缺实测功率也能按峰值定价；
		// 这与触发表中「零功率且无实测」一行构成同口径的正反两面。
		peak := uint32(600)
		fee, err := PriceActual(tierRule(ModeServerMaxPower, actualTiers()),
			ActualMeter{StartedAt: actualStart, EndedAt: actualStart.Add(time.Hour), ChargedWh: 1, ChargedSeconds: 3600,
				Segments: []MeterSegment{{StartedAt: actualStart, EndedAt: actualStart.Add(time.Hour), EnergyWh: 1, PeakW: peak}}})
		if err != nil {
			t.Fatalf("峰值可验证的段应能计价：%v", err)
		}
		if fee.ElectricCents != 180 || fee.ServiceCents != 30 || fee.TotalCents != 210 {
			t.Fatalf("费用 %+v，应为第二档一小时 180+30 分", fee)
		}
	})
	t.Run("分段数恰在上限", func(t *testing.T) {
		fee, err := PriceActual(energyRule(Period{EndMinute: 1440, ElectricCents: 100}),
			ActualMeter{StartedAt: actualStart, EndedAt: actualStart.Add(10080 * time.Second), Segments: secondSegments(actualStart, 10080)})
		if err != nil {
			t.Fatalf("分段数等于上限时应放行：%v", err)
		}
		if fee.TotalCents != 0 {
			t.Fatalf("零电量的费用应为 0 分，得到 %d 分", fee.TotalCents)
		}
	})
	t.Run("实测功率分档计价", func(t *testing.T) {
		power := uint32(1500)
		fee, err := PriceActual(tierRule(ModeServerMaxPower, actualTiers()),
			ActualMeter{StartedAt: actualStart, EndedAt: actualStart.Add(2 * time.Hour), ChargedWh: 100, ChargedSeconds: 7200,
				Segments: []MeterSegment{{StartedAt: actualStart, EndedAt: actualStart.Add(2 * time.Hour), EnergyWh: 100, PeakW: power, PowerW: &power}}})
		if err != nil {
			t.Fatalf("带实测功率的段应能计价：%v", err)
		}
		if fee.ElectricCents != 360 || fee.ServiceCents != 60 || fee.TotalCents != 420 {
			t.Fatalf("费用 %+v，应为第二档两小时 360+60 分", fee)
		}
	})
}
