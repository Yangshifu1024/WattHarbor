package stop

import (
	"slices"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/charging/measurement"
	rules "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/devices"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

// measuredMeter 用最新读数和遥测能佐证的各段数据，
// 为运行中的会话拼出计量记录，
// 让消费上限判定与结算对同一份测量结果定价。
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
	at          time.Time
	wh, seconds uint32
}

func latestMeter(samples []protocol.Event, port uint8, now time.Time) (meterReading, bool) {
	for _, e := range slices.Backward(samples) {
		if now.Sub(e.ReceivedAt) > 30*time.Second {
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

func noPowerForMinute(samples []protocol.Event, port uint8, now time.Time) bool {
	if len(samples) == 0 || now.Sub(samples[len(samples)-1].ReceivedAt) > 30*time.Second {
		return false
	}
	var since time.Time
	for _, e := range samples {
		if e.Type != protocol.Heartbeat {
			continue
		}
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
	return !since.IsZero() && now.Sub(since) >= time.Minute
}
