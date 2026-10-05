package charging

import (
	"errors"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/devices"
)

// 本文件覆盖 MeasuredSegments 的起充时刻容差。
//
// 背景：DC589 按功率分档计价，最终结算要求每段都带可验证功率，
// 分段只能由网关心跳采样构造。结算侧传入的 start 是 charge_order.started_at，
// 来自 START 命令回执的 ack_at；span.StartedAt 则是设备在结束帧里自报的会话起点。
// 两者由不同观测者独立记录，起充后第一条心跳落在 start 之前是常态——
// 隔离栈 2026-10-05 的用户主动停机单就是差 1 秒。该单因此拿不到分段、
// 整单转人工计量复核，无法自动退差额。
//
// 这里用那笔单的原始日志数据做正向证据，再用反向用例锁住放宽的边界。

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

// m2Heartbeats 逐条取自 .tmp/iso-logs/gateway.log 的心跳遥测帧，
// 是实测数据而不是按理想模型编的：接收时刻与设备秒表之间恒定差 1 秒，
// 电量单调不回退，功率全程 150 W，最后一次心跳后设备又充了 7 秒才停。
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

// m2Span 是设备在结束帧自报的收口证据：22:49:54 起、22:53:08 止、194 秒、8 Wh。
// 结束帧的时长与累计秒数必须自洽，这一条实测成立，也是能构造分段的前提。
func m2Span() devices.SessionSpan {
	return devices.SessionSpan{
		DeviceID:       "5348240514082652",
		Port:           2,
		StartedAt:      m2SessionStart,
		EndedAt:        time.Date(2026, 10, 5, 14, 53, 8, 0, time.UTC),
		ChargedSeconds: 194,
		EnergyMilliKWh: 8,
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
	// 末段必须收口到设备自报的结束时刻，最后 7 秒的电量也要有归属。
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
	if total != m2Span().EnergyMilliKWh {
		t.Fatalf("分段合计 %d Wh 与设备自报 %d Wh 不符", total, m2Span().EnergyMilliKWh)
	}
}

func TestUserStoppedSessionPricesWithoutMeterReview(t *testing.T) {
	// 这条用例锁的是业务结果：用户主动停机的单子必须能自动出账。
	// 同样的计量在没有分段时会被 actual.go 以 ErrMeterReview 拒掉，
	// 那正是这批订单此前只能转人工的原因。
	rule := Rule{ID: 7, Version: 1, Spec: Spec{Mode: ModeServerMaxPower, Electric: &ElectricLine{
		Basis: BasisMaxPower, Periods: []Period{{EndMinute: 1440, Tiers: []Tier{
			{MaxWatts: 200, ElectricCents: 50, ServiceCents: 10},
			{MaxWatts: 3500, ElectricCents: 200, ServiceCents: 30},
		}}}}}}
	span := m2Span()
	segments := MeasuredSegments(m2RecordedStart, span, m2Samples(m2Heartbeats()))

	withSegments := ActualMeter{StartedAt: m2RecordedStart, EndedAt: span.EndedAt,
		ChargedWh: span.EnergyMilliKWh, ChargedSeconds: span.ChargedSeconds, Segments: segments}
	fee, err := PriceActual(rule, withSegments)
	if err != nil {
		t.Fatalf("有分段的用户停机单应能自动计价，却报错：%v", err)
	}
	if fee.TotalCents <= 0 {
		t.Fatalf("已实际充过电的订单费用应为正数，得到 %d 分", fee.TotalCents)
	}

	// 对照：去掉分段就是此前的真实路径，必须仍然进复核。
	// 这条对照证明上面的成功来自分段本身，而不是判据被整体放松。
	withoutSegments := withSegments
	withoutSegments.Segments = nil
	if _, err := PriceActual(rule, withoutSegments); !errors.Is(err, ErrMeterReview) {
		t.Fatalf("无分段时仍应要求计量复核，得到 %v", err)
	}
}

// skewShape 决定心跳序列的形状，用来把「起充前窗口」和各种拒绝理由分开，
// 避免某个用例其实被另一条判据挡下、却顶着另一个名字。
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

// startSkewFixture 造一组最小证据：设备在 base 开始计量，服务器在 start 记录起充，
// 心跳按每 20 秒一条上报，接收时刻比设备秒表位置晚 1 秒。
// startOffset 是 start 相对设备自报起点的秒差，正值表示中央记录更晚。
func startSkewFixture(startOffset int, shape skewShape) (time.Time, devices.SessionSpan, []devices.ReadingSample) {
	base := time.Date(2026, 10, 5, 6, 0, 0, 0, time.UTC)
	span := devices.SessionSpan{DeviceID: "D-SKEW", Port: 1, StartedAt: base,
		EndedAt: base.Add(60 * time.Second), ChargedSeconds: 60, EnergyMilliKWh: 4}
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
