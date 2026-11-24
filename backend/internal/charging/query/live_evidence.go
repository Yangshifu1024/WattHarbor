package query

import (
	"slices"
	"time"
	"wattharbor/internal/charging/measurement"
	charging "wattharbor/internal/domain/charging"
	"wattharbor/internal/domain/devices"
	"wattharbor/internal/protocol"
)

// liveMeterEvidence 提取目标设备与端口从起充到当前时刻的最新心跳量，排除未来或其他订单的数据。
// 实时估费可借用相邻功率填入最初 30 秒或中间 180 秒内的缺测区间；该估算不用于结算。
func liveMeterEvidence(device string, port uint8, start time.Time, samples []protocol.Event, now time.Time) (*charging.LiveMeterView, charging.ActualMeter, bool) {
	for i, e := range slices.Backward(samples) {

		if e.Type != protocol.Heartbeat || e.DeviceID != device || e.ReceivedAt.Before(start) || e.ReceivedAt.After(now) {
			continue
		}
		for _, p := range e.ChargingPorts {
			if p.Port == port {
				view := &charging.LiveMeterView{At: e.ReceivedAt, Stale: now.Sub(e.ReceivedAt) > 30*time.Second, KWh: float64(p.ChargedMWh) / 1e6, Seconds: p.ChargedSeconds}
				// 用最后一次上报的累计量作为分段总量，按累计秒数推算设备开始时刻。
				span := devices.SessionSpan{DeviceID: device, Port: port, StartedAt: e.ReceivedAt.Add(-time.Duration(p.ChargedSeconds) * time.Second), EndedAt: e.ReceivedAt, ChargedSeconds: p.ChargedSeconds, EnergyWh: p.ChargedMWh / 1000}
				meter := charging.ActualMeter{StartedAt: start, EndedAt: e.ReceivedAt, ChargedWh: p.ChargedMWh / 1000, ChargedSeconds: p.ChargedSeconds, Segments: charging.MeasuredSegments(start, span, measurement.ReadingSamplesOf(samples[:i+1]))}
				// 实时估算以首次观测负载补齐最初不超过 30 秒的心跳区间。
				// 只填入实时估费分段，结算仍要求实测功率。
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
				// 最多 180 秒的缺测区间沿用之前的功率，只用于实时估费。
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
