// Package measurement 转换设备心跳与结束事件，生成充电计量分段。
package measurement

import (
	"time"

	rules "chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/devices"
	"chargepilot/internal/protocol"
)

// SessionSpanOf 将结束事件转换为设备会话计量，零时刻或结束早于开始时返回 false。
// 事件类型由调用方保证，这里不重复判断；电量字段按 Wh 读取。
func SessionSpanOf(e protocol.Event) (devices.SessionSpan, bool) {
	if e.StartedAt.IsZero() || e.EndedAt.IsZero() || e.EndedAt.Before(e.StartedAt) {
		return devices.SessionSpan{}, false
	}
	return devices.SessionSpan{
		DeviceID:       e.DeviceID,
		Port:           e.Port,
		StartedAt:      e.StartedAt,
		EndedAt:        e.EndedAt,
		ChargedSeconds: e.ChargedSeconds,
		EnergyWh:       e.EnergyMilliKWh,
	}, true
}

// ReadingSamplesOf 按顺序转换心跳读数，跳过其他事件。
// 功率由 0.1 W 换算为 W 并四舍五入，零功率保留为有效读数，不视为缺测。
func ReadingSamplesOf(events []protocol.Event) []devices.ReadingSample {
	out := make([]devices.ReadingSample, 0, len(events))
	for _, e := range events {
		if e.Type != protocol.Heartbeat {
			continue
		}
		sample := devices.ReadingSample{
			DeviceID:   e.DeviceID,
			ReceivedAt: e.ReceivedAt,
			Readings:   make([]devices.Reading, 0, len(e.ChargingPorts)),
		}
		for _, p := range e.ChargingPorts {
			sample.Readings = append(sample.Readings, devices.Reading{
				Port:           p.Port,
				At:             e.ReceivedAt,
				ChargedSeconds: p.ChargedSeconds,
				ChargedMWh:     p.ChargedMWh,
				PowerW:         (p.PowerDeciWatts + 5) / 10,
			})
		}
		out = append(out, sample)
	}
	return out
}

// MeasuredSegmentsOf 按输入顺序转换心跳，结合起充与结束时间生成计量分段。
// 起止时间或读数无效时返回 nil，不补造计量。
func MeasuredSegmentsOf(start time.Time, terminal protocol.Event, events []protocol.Event) []rules.MeterSegment {
	span, ok := SessionSpanOf(terminal)
	if !ok {
		return nil
	}
	return rules.MeasuredSegments(start, span, ReadingSamplesOf(events))
}
