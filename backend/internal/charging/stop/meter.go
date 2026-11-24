package stop

import (
	"slices"
	"time"

	"chargepilot/internal/charging/measurement"
	gatewaycontract "chargepilot/internal/contracts/gateway"
	rules "chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/devices"
	"chargepilot/internal/protocol"
)

// measuredMeter 用订单开始时刻、最新读数与完整心跳生成运行中计量。
// 使用与结算相同的分段校验，不补造读数。
func measuredMeter(order AutoStopOrder, latest meterReading, samples []protocol.Event) rules.ActualMeter {
	ended := latest.at
	meter := rules.ActualMeter{
		StartedAt:      order.StartedAt,
		EndedAt:        ended,
		ChargedWh:      latest.wh,
		ChargedSeconds: latest.seconds,
	}
	// 用最新读数校验分段总量，并由累计秒数推算设备开始时刻。
	span := devices.SessionSpan{
		DeviceID: order.DeviceID, Port: order.PortNo,
		StartedAt:      ended.Add(-time.Duration(latest.seconds) * time.Second),
		EndedAt:        ended,
		ChargedSeconds: latest.seconds, EnergyWh: latest.wh,
	}
	meter.Segments = rules.MeasuredSegments(order.StartedAt, span, measurement.ReadingSamplesOf(samples))
	return meter
}

// meterReading 保存一次心跳的接收时刻、累计 Wh 和累计秒数，供检查费用上限。
type meterReading struct {
	at      time.Time // 心跳接收的 UTC 时刻
	wh      uint32    // 累计电量，单位 Wh
	seconds uint32    // 累计充电时长，单位秒
}

// latestMeter 从按入库顺序排列的 samples 中取目标 port 的最新可用计量。
// now 为 UTC 判定时刻；拒绝未来、陈旧及不能整除为 Wh 的读数，第二返回值表示是否找到。
// 没有合格读数时返回 false，不把缺失当作零用量。
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

// noPowerForMinute 检查目标 port 是否连续一分钟上报零功率。
// samples 必须保留入库顺序及端口缺失事件，now 为 UTC 判定时刻；不修改输入。
// 未来或零时间返回 false；乱序与重复清空累计，后续须晚于已接受的最大时间才能建立独立新窗口。
// 缺端口、正功率或长间隔会重新开始累计零功率时长，不继承异常前的证据。
// 最后心跳须未过期；只计算心跳覆盖的时间，末次上报后的静默不算零功率。
func noPowerForMinute(samples []protocol.Event, port uint8, now time.Time) bool {
	if len(samples) == 0 {
		return false
	}
	var since, previous time.Time
	for _, e := range samples {
		if e.Type != protocol.Heartbeat {
			continue
		}
		if e.ReceivedAt.IsZero() || e.ReceivedAt.After(now) {
			return false
		}
		if !previous.IsZero() && !e.ReceivedAt.After(previous) {
			// 保留最大已接受时刻，异常样本不成为新起点，也不能把累计基准向过去移动。
			since = time.Time{}
			continue
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
			// 其他端口的读数无法证明这个端口没有电。
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
