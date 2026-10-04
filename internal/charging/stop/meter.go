package stop

import (
	"slices"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/charging/measurement"
	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	rules "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/devices"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

// measuredMeter 用最新读数和遥测能佐证的各段数据，
// 为运行中的会话拼出计量记录，
// order 提供原开始时刻与端口，latest 提供最新计量，samples 为完整事件序列。
// 返回实际计量及可验证分段，不执行 IO；消费上限判定与结算使用同一份证据。
func measuredMeter(order AutoStopOrder, latest meterReading, samples []protocol.Event) rules.ActualMeter {
	ended := latest.at
	meter := rules.ActualMeter{
		StartedAt:      order.StartedAt,
		EndedAt:        ended,
		ChargedWh:      latest.wh,
		ChargedSeconds: latest.seconds,
	}
	// 把当前读数当作收口证据交给计量校验，不生成额外测量。
	span := devices.SessionSpan{
		DeviceID: order.DeviceID, Port: order.PortNo,
		StartedAt:      ended.Add(-time.Duration(latest.seconds) * time.Second),
		EndedAt:        ended,
		ChargedSeconds: latest.seconds, EnergyMilliKWh: latest.wh,
	}
	meter.Segments = rules.MeasuredSegments(order.StartedAt, span, measurement.ReadingSamplesOf(samples))
	return meter
}

type meterReading struct {
	at      time.Time // 遥测接收的 UTC 时刻
	wh      uint32    // 累计电量，单位 Wh
	seconds uint32    // 累计充电时长，单位秒
}

// latestMeter 从按入库顺序排列的 samples 中取目标 port 的最新可用计量。
// now 为 UTC 判定时刻；拒绝未来、陈旧及不能整除为 Wh 的读数，第二返回值表示是否找到。
// 不执行 IO，不以零值代替缺失证据。
func latestMeter(samples []protocol.Event, port uint8, now time.Time) (meterReading, bool) {
	for _, e := range slices.Backward(samples) {
		if e.ReceivedAt.After(now) || now.Sub(e.ReceivedAt) > gatewaycontract.MaxChargingEvidenceGap {
			break
		}
		for _, p := range e.ChargingPorts {
			if p.Port == port && p.ChargedMWh%1000 == 0 {
				return meterReading{at: e.ReceivedAt, wh: p.ChargedMWh / 1000, seconds: p.ChargedSeconds}, true
			}
		}
	}
	return meterReading{}, false
}

// noPowerForMinute 判断目标 port 是否有连续一分钟的零功率心跳证据。
// samples 必须保留入库顺序及端口缺失事件，now 为 UTC 判定时刻；不修改输入。
// 返回 false 表示证据不足或不可信，包括未来、乱序、重复时刻、间隔过大及末条陈旧。
// 连续窗口以末条证据为结束，不能把末条之后的静默时间计作已观测零功率。
func noPowerForMinute(samples []protocol.Event, port uint8, now time.Time) bool {
	if len(samples) == 0 {
		return false
	}
	var since, previous time.Time
	for _, e := range samples {
		if e.Type != protocol.Heartbeat {
			continue
		}
		if e.ReceivedAt.IsZero() || e.ReceivedAt.After(now) || (!previous.IsZero() && !e.ReceivedAt.After(previous)) {
			return false
		}
		if !previous.IsZero() && e.ReceivedAt.Sub(previous) > gatewaycontract.MaxChargingEvidenceGap {
			since = time.Time{}
		}
		previous = e.ReceivedAt
		seen, positive := false, false
		for _, p := range e.ChargingPorts {
			if p.Port == port {
				seen = true
				positive = p.PowerDeciWatts > 0
				break
			}
		}
		if !seen {
			// 其他端口的遥测无法证明这个端口没有电。
			since = time.Time{}
			continue
		}
		if positive {
			since = time.Time{}
		} else if since.IsZero() {
			since = e.ReceivedAt
		}
	}
	return !since.IsZero() && now.Sub(previous) <= gatewaycontract.MaxChargingEvidenceGap && previous.Sub(since) >= time.Minute
}
