package query

import (
	"chargepilot/internal/charging/measurement"
	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/devices"
	"chargepilot/internal/protocol"
	"slices"
	"time"
)

// liveMeterEvidence 从 samples 原序心跳为 device/port 提取 start 至 now 最后观测量。
// 返回实时投影、估算计量及是否有目标端口证据；未来／旧单／其它端口被排除，无 IO。
// 只为实时估算补最初30秒及中间180秒以内的短缺口，不修改最终结算证据构造。
func liveMeterEvidence(device string, port uint8, start time.Time, samples []protocol.Event, now time.Time) (*charging.LiveMeterView, charging.ActualMeter, bool) {
	for i, e := range slices.Backward(samples) {

		if e.Type != protocol.Heartbeat || e.DeviceID != device || e.ReceivedAt.Before(start) || e.ReceivedAt.After(now) {
			continue
		}
		for _, p := range e.ChargingPorts {
			if p.Port == port {
				view := &charging.LiveMeterView{At: e.ReceivedAt, Stale: now.Sub(e.ReceivedAt) > 30*time.Second, KWh: float64(p.ChargedMWh) / 1e6, Seconds: p.ChargedSeconds}
				// 实时估算用截至本次上报的累计量作为收口证据。
				span := devices.SessionSpan{DeviceID: device, Port: port, StartedAt: e.ReceivedAt.Add(-time.Duration(p.ChargedSeconds) * time.Second), EndedAt: e.ReceivedAt, ChargedSeconds: p.ChargedSeconds, EnergyWh: p.ChargedMWh / 1000}
				meter := charging.ActualMeter{StartedAt: start, EndedAt: e.ReceivedAt, ChargedWh: p.ChargedMWh / 1000, ChargedSeconds: p.ChargedSeconds, Segments: charging.MeasuredSegments(start, span, measurement.ReadingSamplesOf(samples[:i+1]))}
				// 实时估算以首次观测负载补齐最初不超过 30 秒的心跳区间。
				// 最终结算仍使用严格的计量证据校验。
				if len(meter.Segments) > 0 && meter.Segments[0].PowerW == nil && meter.Segments[0].EndedAt.Sub(start) <= 30*time.Second {
					for _, sample := range samples[:i+1] {
						found := false
						for _, initial := range sample.ChargingPorts {
							if initial.Port == port && sample.DeviceID == device {
								power := (initial.PowerDeciWatts + 5) / 10
								meter.Segments[0].PowerW = &power
								meter.Segments[0].PeakW = power
								found = true
								break
							}
						}
						if found {
							break
						}
					}
				}
				// 短采样缺口仅用于实时估算，沿用前一观测负载；最终结算仍保持严格证据缺口。
				for j := 1; j < len(meter.Segments); j++ {
					segment := &meter.Segments[j]
					previous := meter.Segments[j-1]
					if segment.PowerW == nil && segment.EndedAt.Sub(segment.StartedAt) <= 180*time.Second && previous.PowerW != nil {
						power := *previous.PowerW
						for _, sample := range samples[:i+1] {
							for _, reading := range sample.ChargingPorts {
								if reading.Port == port && sample.DeviceID == device {
									at := span.StartedAt.Add(time.Duration(reading.ChargedSeconds) * time.Second)
									if !at.After(segment.StartedAt) {
										power = (reading.PowerDeciWatts + 5) / 10
									}
								}
							}
						}
						segment.PowerW = &power
						segment.PeakW = power
						for _, sample := range samples[:i+1] {
							for _, reading := range sample.ChargingPorts {
								if reading.Port == port && sample.DeviceID == device {
									at := span.StartedAt.Add(time.Duration(reading.ChargedSeconds) * time.Second)
									if at.After(segment.StartedAt) && !at.After(segment.EndedAt) {
										segment.PeakW = max(segment.PeakW, (reading.PowerDeciWatts+5)/10)
									}
								}
							}
						}
					}
				}
				if p.ChargedMWh%1000 != 0 {
					meter.ReviewRequired = true
				}
				return view, meter, true
			}
		}
	}
	return nil, charging.ActualMeter{}, false
}
