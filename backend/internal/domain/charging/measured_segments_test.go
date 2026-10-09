package charging

import (
	"errors"
	"testing"
	"time"

	"chargepilot/internal/domain/devices"
)

// 覆盖中央起充时间与设备时间的偏差，复现两者相差一秒时仍可生成合法分段。
// 回归数据来自隔离开发栈日志，只证明计量规则，不代表真实设备验收。

// m2SessionStart 是隔离栈 M2 订单（设备 5348240514082652 端口 2）的设备自报会话起点。
// 时间取自结束帧 started_at 字段 2026-10-05T22:49:54+08:00。
var m2SessionStart = time.Date(2026, 10, 5, 14, 49, 54, 0, time.UTC)

// m2RecordedStart 是中央记录的起充时刻，取自 START 命令回执 ack_at
// （internal/worker/charge/sync.go 把 row.AckAt 写进 charge_order.started_at）。
// 设备先按自身时钟起充、服务器稍后才等到回执，因此它比设备自报晚 1 秒。
var m2RecordedStart = time.Date(2026, 10, 5, 14, 49, 55, 0, time.UTC)

// m2Reading 是 M2 的一条心跳读数：接收时刻、设备累计秒数与累计电量。
type m2Reading struct {
	received time.Time
	seconds  uint32
	wh       uint32
}

// m2Heartbeats 取自隔离栈 gateway.log：接收时间比设备计时晚一秒。
// 电量单调增加，功率为 150 W，最后一条心跳后七秒结束。
func m2Heartbeats() []m2Reading {
	return []m2Reading{
		{time.Date(2026, 10, 5, 14, 49, 55, 0, time.UTC), 0, 0},
		{time.Date(2026, 10, 5, 14, 49, 55, 0, time.UTC), 0, 0},
		{time.Date(2026, 10, 5, 14, 50, 10, 0, time.UTC), 15, 0},
		{time.Date(2026, 10, 5, 14, 50, 25, 0, time.UTC), 30, 1},
		{time.Date(2026, 10, 5, 14, 50, 40, 0, time.UTC), 45, 1},
		{time.Date(2026, 10, 5, 14, 50, 55, 0, time.UTC), 60, 2},
		{time.Date(2026, 10, 5, 14, 51, 10, 0, time.UTC), 75, 3},
		{time.Date(2026, 10, 5, 14, 51, 25, 0, time.UTC), 90, 3},
		{time.Date(2026, 10, 5, 14, 51, 30, 0, time.UTC), 95, 3},
		{time.Date(2026, 10, 5, 14, 51, 45, 0, time.UTC), 110, 4},
		{time.Date(2026, 10, 5, 14, 52, 0, 0, time.UTC), 125, 5},
		{time.Date(2026, 10, 5, 14, 52, 15, 0, time.UTC), 140, 5},
		{time.Date(2026, 10, 5, 14, 52, 30, 0, time.UTC), 155, 6},
		{time.Date(2026, 10, 5, 14, 52, 32, 0, time.UTC), 157, 6},
		{time.Date(2026, 10, 5, 14, 52, 47, 0, time.UTC), 172, 7},
		{time.Date(2026, 10, 5, 14, 53, 2, 0, time.UTC), 187, 7},
	}
}

// m2Samples 把心跳读数转成领域读数样本，设备与端口按 M2 实际值填写。
func m2Samples(readings []m2Reading) []devices.ReadingSample {
	samples := make([]devices.ReadingSample, 0, len(readings))
	for _, r := range readings {
		samples = append(samples, devices.ReadingSample{
			DeviceID:   "5348240514082652",
			ReceivedAt: r.received,
			Readings: []devices.Reading{{
				Port: 2, At: r.received, ChargedSeconds: r.seconds, ChargedMWh: r.wh * 1000, PowerW: 150,
			}},
		})
	}
	return samples
}

// m2Span 保存设备结束帧的计量：22:49:54 起、22:53:08 止、194 秒、8 Wh。
// 结束帧的时长与累计秒数必须自洽，这一条实测成立，也是能构造分段的前提。
func m2Span() devices.SessionSpan {
	return devices.SessionSpan{
		DeviceID:       "5348240514082652",
		Port:           2,
		StartedAt:      m2SessionStart,
		EndedAt:        time.Date(2026, 10, 5, 14, 53, 8, 0, time.UTC),
		ChargedSeconds: 194,
		EnergyWh:       8,
	}
}

func TestMeasuredSegmentsAcceptsDeviceStartOneSecondBeforeRecordedStart(t *testing.T) {
	segments := MeasuredSegments(m2RecordedStart, m2Span(), m2Samples(m2Heartbeats()))
	if len(segments) == 0 {
		t.Fatal("设备自报起点比中央记录早 1 秒且该秒无电量时，分段不应被整段拒绝")
	}
	// 首段必须从中央记录的起充时刻开始：结算侧 actual.go 要求首段起点与
	// meter.StartedAt（即 charge_order.started_at）逐秒相等，否则照样进复核。
	if !segments[0].StartedAt.Equal(m2RecordedStart) {
		t.Fatalf("首段起点 %s 应等于中央记录的起充时刻 %s", segments[0].StartedAt, m2RecordedStart)
	}
	// 最后一段须延续到设备上报的结束时刻，并计入最后 7 秒的电量。
	if !segments[len(segments)-1].EndedAt.Equal(m2Span().EndedAt) {
		t.Fatalf("末段终点 %s 应等于结束时刻 %s", segments[len(segments)-1].EndedAt, m2Span().EndedAt)
	}
	// 分段必须首尾相接且无空洞，缺口由 actual.go 的游标检查逐段核对。
	total, cursor := uint32(0), m2RecordedStart
	for i, s := range segments {
		if !s.StartedAt.Equal(cursor) {
			t.Fatalf("第 %d 段起点 %s 与上一段终点 %s 不相接", i, s.StartedAt, cursor)
		}
		if s.EndedAt.Before(s.StartedAt) {
			t.Fatalf("第 %d 段终点早于起点：%s → %s", i, s.StartedAt, s.EndedAt)
		}
		// DC589 走 server_max_power：每段都必须有功率，否则该段无法定价。
		if s.PowerW == nil {
			t.Fatalf("第 %d 段缺少功率，无法按功率分档计价", i)
		}
		total += s.EnergyWh
		cursor = s.EndedAt
	}
	// 分段求和必须等于设备自报总量，一瓦都不能凭空多出或少掉。
	if total != m2Span().EnergyWh {
		t.Fatalf("分段合计 %d Wh 与设备自报 %d Wh 不符", total, m2Span().EnergyWh)
	}
}

func TestUserStoppedSessionPricesWithoutMeterReview(t *testing.T) {
	// 验证合法分段可自动计价；同一记录缺少分段时仍须返回 ErrMeterReview。
	rule := Rule{ID: 7, Version: 1, Spec: Spec{Mode: ModeServerMaxPower, Electric: &ElectricLine{
		Basis: BasisMaxPower, Periods: []Period{{EndMinute: 1440, Tiers: []Tier{
			{MaxWatts: 200, ElectricCents: 50, ServiceCents: 10},
			{MaxWatts: 3500, ElectricCents: 200, ServiceCents: 30},
		}}}}}}
	span := m2Span()
	segments := MeasuredSegments(m2RecordedStart, span, m2Samples(m2Heartbeats()))

	withSegments := ActualMeter{StartedAt: m2RecordedStart, EndedAt: span.EndedAt,
		ChargedWh: span.EnergyWh, ChargedSeconds: span.ChargedSeconds, Segments: segments}
	fee, err := PriceActual(rule, withSegments)
	if err != nil {
		t.Fatalf("有分段的用户停机单应能自动计价，却报错：%v", err)
	}
	if fee.TotalCents <= 0 {
		t.Fatalf("已实际充过电的订单费用应为正数，得到 %d 分", fee.TotalCents)
	}

	// 去掉分段后仍须复核，防止放宽其他计量检查。
	withoutSegments := withSegments
	withoutSegments.Segments = nil
	if _, err := PriceActual(rule, withoutSegments); !errors.Is(err, ErrMeterReview) {
		t.Fatalf("无分段时仍应要求计量复核，得到 %v", err)
	}
}

// skewShape 控制心跳时间与电量变化，使各用例只触发目标拒绝条件。
type skewShape int

const (
	// skewMonotonic：常规序列，电量单调递增、时刻单调递增。
	skewMonotonic skewShape = iota
	// skewEnergyBeforeStart：中央记录的起充比设备首条读数晚，读数还带着电量增量。
	// 这是「服务器漏记了真实起充」的情形。
	skewEnergyBeforeStart
	// skewRewindAfterStart：起充之后时刻回退，但电量没变。
	skewRewindAfterStart
)

// startSkewFixture 构造设备从 base 计时、服务器在 start 记录起充的心跳。
// 心跳按每 20 秒一条上报，接收时刻比设备秒表位置晚 1 秒。
// startOffset 是 start 相对设备自报起点的秒差，正值表示中央记录更晚。
func startSkewFixture(startOffset int, shape skewShape) (time.Time, devices.SessionSpan, []devices.ReadingSample) {
	base := time.Date(2026, 10, 5, 6, 0, 0, 0, time.UTC)
	span := devices.SessionSpan{DeviceID: "D-SKEW", Port: 1, StartedAt: base,
		EndedAt: base.Add(60 * time.Second), ChargedSeconds: 60, EnergyWh: 4}
	type step struct {
		seconds uint32
		wh      uint32
	}
	steps := []step{{0, 0}, {20, 1}, {40, 2}, {60, 4}}
	switch shape {
	case skewEnergyBeforeStart:
		// 首条读数在 start 之前且已经累计 2 Wh：中央把起充记晚了。
		steps = []step{{0, 2}, {20, 3}, {40, 4}, {60, 4}}
	case skewRewindAfterStart:
		// 40 秒处报过之后再补一条 20 秒的读数：电量没变但时刻回退。
		steps = []step{{0, 0}, {40, 2}, {20, 2}, {60, 4}}
	}
	samples := make([]devices.ReadingSample, 0, len(steps))
	for _, s := range steps {
		at := base.Add(time.Duration(s.seconds) * time.Second)
		samples = append(samples, devices.ReadingSample{
			DeviceID:   "D-SKEW",
			ReceivedAt: at.Add(time.Second),
			Readings:   []devices.Reading{{Port: 1, At: at, ChargedSeconds: s.seconds, ChargedMWh: s.wh * 1000, PowerW: 600}},
		})
	}
	return base.Add(time.Duration(startOffset) * time.Second), span, samples
}

// tailFixture 构造一条每 20 秒上报一次、60 秒后停止上报的充电记录：
// 前 60 秒充了 3 Wh，其余电量由结束帧自报补齐，模拟心跳丢失后的尾巴。
// endSeconds 是会话总时长，也决定最后一条心跳到结束帧的间隔（endSeconds − 60）。
func tailFixture(endSeconds uint32) (time.Time, devices.SessionSpan, []devices.ReadingSample) {
	base := time.Date(2026, 10, 5, 6, 0, 0, 0, time.UTC)
	span := devices.SessionSpan{DeviceID: "D-TAIL", Port: 1, StartedAt: base,
		EndedAt: base.Add(time.Duration(endSeconds) * time.Second), ChargedSeconds: endSeconds, EnergyWh: 6}
	steps := []struct {
		seconds uint32
		wh      uint32
	}{{0, 0}, {20, 1}, {40, 2}, {60, 3}}
	samples := make([]devices.ReadingSample, 0, len(steps))
	for _, s := range steps {
		at := base.Add(time.Duration(s.seconds) * time.Second)
		samples = append(samples, devices.ReadingSample{
			DeviceID:   "D-TAIL",
			ReceivedAt: at.Add(time.Second),
			Readings:   []devices.Reading{{Port: 1, At: at, ChargedSeconds: s.seconds, ChargedMWh: s.wh * 1000, PowerW: 600}},
		})
	}
	return base, span, samples
}

// tailRule 是功率分档计价规则：600 W 落第二档。费率取整除后为整数分的值。
// 使费用断言不含舍入因素。
func tailRule() Rule {
	return Rule{ID: 7, Version: 1, Spec: Spec{Mode: ModeServerMaxPower, Electric: &ElectricLine{
		Basis: BasisMaxPower, Periods: []Period{{EndMinute: 1440, Tiers: []Tier{
			{MaxWatts: 200, ElectricCents: 100, ServiceCents: 20},
			{MaxWatts: 3500, ElectricCents: 180, ServiceCents: 30},
		}}}}}}
}

// TestTailSegmentPowerJoinsOnlyWithinWindow 验证尾段最多沿用前次功率 60 秒。
// 间隔更长时保留功率缺测，不从电量反推；功率计费必须转计量复核。
func TestTailSegmentPowerJoinsOnlyWithinWindow(t *testing.T) {
	t.Run("间隔恰为六十秒时尾段沿用前次功率", func(t *testing.T) {
		start, span, samples := tailFixture(120)
		segments := MeasuredSegments(start, span, samples)
		if len(segments) == 0 {
			t.Fatal("心跳齐备且尾段间隔等于窗口时，分段不应被拒绝")
		}
		tail := segments[len(segments)-1]
		if tail.PowerW == nil || *tail.PowerW != 600 {
			t.Fatalf("尾段应沿用前次功率 600 W，得到 %v", tail.PowerW)
		}
		if tail.EnergyWh != 3 {
			t.Fatalf("尾段电量应为设备自报补齐的 3 Wh，得到 %d Wh", tail.EnergyWh)
		}
		// 沿用功率的尾段必须能直接计价：120 秒全按 600 W 落第二档，180+30 分/时。
		fee, err := PriceActual(tailRule(), ActualMeter{StartedAt: start, EndedAt: span.EndedAt,
			ChargedWh: span.EnergyWh, ChargedSeconds: span.ChargedSeconds, Segments: segments})
		if err != nil {
			t.Fatalf("尾段沿用功率后应能自动计价：%v", err)
		}
		if fee.ElectricCents != 6 || fee.ServiceCents != 1 || fee.TotalCents != 7 {
			t.Fatalf("费用 %+v，应为 180+30 分/时下 120 秒计 6+1 分", fee)
		}
	})
	t.Run("间隔超过六十秒时尾段无功率并整单进复核", func(t *testing.T) {
		start, span, samples := tailFixture(121)
		segments := MeasuredSegments(start, span, samples)
		if len(segments) == 0 {
			t.Fatal("超窗只影响功率不影响电量归属，分段本身不应被拒绝")
		}
		tail := segments[len(segments)-1]
		if tail.PowerW != nil || tail.PeakW != 0 {
			t.Fatalf("超窗尾段不得沿用功率或从电量反推，得到 PowerW=%v PeakW=%d", tail.PowerW, tail.PeakW)
		}
		if tail.EnergyWh != 3 {
			t.Fatalf("尾段电量仍应由结束帧自报补齐 3 Wh，得到 %d Wh", tail.EnergyWh)
		}
		if _, err := PriceActual(tailRule(), ActualMeter{StartedAt: start, EndedAt: span.EndedAt,
			ChargedWh: span.EnergyWh, ChargedSeconds: span.ChargedSeconds, Segments: segments}); !errors.Is(err, ErrMeterReview) {
			t.Fatalf("功率分档下超窗尾段缺功率应进计量复核，得到 %v", err)
		}
	})
}

func TestMeasuredSegmentsStartSkewTolerance(t *testing.T) {
	cases := []struct {
		name        string
		startOffset int // start 相对设备自报起点的秒差
		shape       skewShape
		want        bool // 是否应当构造出分段
		why         string
	}{
		{name: "起充早于设备自报起点", startOffset: -1, want: true,
			why: "中央比设备慢 1 秒记录起充，起充前无电量，应当放行"},
		{name: "起充与设备自报同刻", startOffset: 0, want: true,
			why: "两方一致时不受影响"},
		{name: "容差内的偏移", startOffset: 4, want: true,
			why: "函数开头已接受 5 秒时钟偏差，容差内的读数不应再被自己否掉"},
		{name: "起充前已有电量", startOffset: 1, shape: skewEnergyBeforeStart, want: false,
			why: "start 之前出现电量增量说明服务器漏记了真实起充，属证据矛盾"},
		{name: "偏移超出时钟容差", startOffset: 6, want: false,
			why: "设备自报起点与中央记录差 6 秒，超出容差仍须整段拒绝"},
		{name: "起充后时刻回退", shape: skewRewindAfterStart, want: false,
			why: "已经开始计量后时刻再回退，放宽只针对起充前的窗口"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, span, samples := startSkewFixture(tc.startOffset, tc.shape)
			got := MeasuredSegments(start, span, samples)
			if tc.want && len(got) == 0 {
				t.Fatalf("应构造出分段却返回 nil：%s", tc.why)
			}
			if !tc.want && len(got) != 0 {
				t.Fatalf("应拒绝却构造出 %d 段：%s", len(got), tc.why)
			}
		})
	}
}
