// 遥测样本转换与会话时间范围校验，供用户读模型和 worker 同步复用。
package measurement

import (
	"time"

	rules "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/devices"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

// SessionSpanOf 将 e 的起止时刻、设备、端口、秒数及 milli-kWh 电量转换为收口证据。
// 返回 false 表示零时刻或结束早于开始；不修改输入，不执行 IO。
//
// 不校验事件类型：调用方手上已是收口事件（来自结算回调或实时估算），
// 在此重复判断类型只会让"构造方式不同"被误判成"证据缺失"。
// 类型的合法性由该事件本身的产出方保证。
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
		EnergyMilliKWh: e.EnergyMilliKWh,
	}, true
}

// ReadingSamplesOf 将 events 的心跳按原序转换为领域读数；不修改输入，不执行 IO。
// 非心跳事件直接跳过，
// 返回的切片只含可用于计量的样本。
//
// 功率以 0.1 W 为单位上报，此处换算成 W 并四舍五入。换算结果为 0
// 表示设备确实上报了 0 W 而不是缺测，故保留该读数。
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

// MeasuredSegmentsOf 使用 UTC 开始时刻 start、收口 terminal 和原序 events 生成计量分段。
// 返回 nil 表示起止或分段证据不成立；不执行 IO，也不生成补偿读数。
// 把协议事件翻译成领域读数后交给
// 领域计量规则计算。
//
// 翻译与计算的分界在此：领域层只认 devices 的读数类型，不感知协议存在；
// 用例层承担翻译，不把协议字段渗进计费规则。证据不成立时领域规则返回 nil，
// 调用方据此进入计量复核而不是猜一个值。
func MeasuredSegmentsOf(start time.Time, terminal protocol.Event, events []protocol.Event) []rules.MeterSegment {
	span, ok := SessionSpanOf(terminal)
	if !ok {
		return nil
	}
	return rules.MeasuredSegments(start, span, ReadingSamplesOf(events))
}
