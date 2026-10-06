package charging

import (
	"github.com/ChargePilot2026/charge-pilot/internal/domain/devices"
	"time"
)

// maxSamples 是一次充电允许的最大心跳条数。上限防止异常设备用海量
// 上报把一次计费放大成无界计算，也对应网关侧的取数分页上限。
const maxSamples = 10080

// clockSkewTolerance 允许设备时钟与充电开始时刻的最大偏差。
// 超出即判定证据自相矛盾，整段拒绝而不是取其一。
const clockSkewTolerance = 5 * time.Second

// powerJoinWindow 相邻心跳间隔不超过该值时，才用前次功率描述该区间。
// 更长的间隔保留为证据缺失，不从电量反推功率。
const powerJoinWindow = 60 * time.Second

// MeasuredSegments 按心跳读数把一次充电切成计量分段。
//
// C2 没有累计电表，只有完整的心跳测量值可用。设备侧的已用秒数把每条读数
// 定位到设备时钟上；接收时间只用来剔除过期或前后矛盾的上报，
// 绝不用来分摊 Wh。
//
// start 是调用方记录的实际起充时刻（结算侧为 charge_order.started_at，
// 与 START 命令回执同源），span.StartedAt 则是设备在结束帧里自报的会话起点。
// 两者由不同的观测者独立记录，允许 clockSkewTolerance 内的偏差：起充后
// 第一条心跳常常落在 start 之前一两秒，因为设备按自己的时钟先开始计量、
// 服务器才等到回执。这类读数在下面按「无新增电量」跳过，不移动收口游标。
//
// 任一环节的证据不成立（时长与累计秒数不符、时钟漂移、精度不足、读数回退、
// 分段求和对不上总量）都返回 nil，让调用方进入计量复核，而不是猜一个值。
func MeasuredSegments(start time.Time, span devices.SessionSpan, samples []devices.ReadingSample) []MeterSegment {
	if start.IsZero() || span.StartedAt.IsZero() || span.EndedAt.Before(start) ||
		span.EndedAt.Sub(start) > 7*24*time.Hour ||
		span.EndedAt.Sub(span.StartedAt) != time.Duration(span.ChargedSeconds)*time.Second ||
		len(samples) > maxSamples {
		return nil
	}
	if delta := start.Sub(span.StartedAt); delta < -clockSkewTolerance || delta > clockSkewTolerance {
		return nil
	}
	cursor, previous := start, uint32(0)
	var previousPower *uint32
	segments := []MeterSegment{}
	for _, sample := range samples {
		if sample.DeviceID != span.DeviceID {
			continue
		}
		for _, reading := range sample.Readings {
			if reading.Port != span.Port {
				continue
			}
			at := span.StartedAt.Add(time.Duration(reading.ChargedSeconds) * time.Second)
			lag := sample.ReceivedAt.Sub(at)
			if lag < -clockSkewTolerance || lag > clockSkewTolerance ||
				reading.ChargedSeconds > span.ChargedSeconds || reading.ChargedMWh%1000 != 0 {
				return nil
			}
			wh := reading.ChargedMWh / 1000
			if wh < previous || wh > span.EnergyWh {
				return nil
			}
			if at.Before(cursor) {
				// 设备按自身时钟计量的起点可以早于服务器记录的起充时刻：开头的时钟容差
				// 已经接受了这两者的偏差，就不能再把容差窗口内的读数判成证据自相矛盾。
				// 只有「落在 start 之前、仍在容差内、本次没有新增累计电量」的读数可以跳过——
				// 设备自己都报告这段时间没有电量，说明它不构成可计费区间，跳过既不移动
				// 收口游标也不改变累计电量，功率读数仍用于描述随后的区间。
				// 一旦越过容差窗口、已经起充后再回退时刻、或出现电量增量，证据仍然拒绝。
				if !at.Before(start) || wh != previous || at.Before(start.Add(-clockSkewTolerance)) {
					return nil
				}
				previousPower = ptr(reading.PowerW)
				continue
			}
			if at.Equal(cursor) {
				if wh != previous {
					return nil
				}
				previousPower = ptr(reading.PowerW)
				continue
			}
			segment := MeterSegment{StartedAt: cursor, EndedAt: at, EnergyWh: wh - previous}
			if previousPower != nil && at.Sub(cursor) <= powerJoinWindow {
				v := *previousPower
				segment.PowerW = &v
				segment.PeakW = max(reading.PowerW, v)
			}
			segments = append(segments, segment)
			previousPower = ptr(reading.PowerW)
			cursor, previous = at, wh
		}
	}
	if len(segments) == 0 {
		return nil
	}
	if cursor.Equal(span.EndedAt) {
		if previous != span.EnergyWh {
			return nil
		}
	} else {
		segment := MeterSegment{StartedAt: cursor, EndedAt: span.EndedAt, EnergyWh: span.EnergyWh - previous}
		if previousPower != nil && span.EndedAt.Sub(cursor) <= powerJoinWindow {
			v := *previousPower
			segment.PowerW = &v
			segment.PeakW = v
		}
		segments = append(segments, segment)
	}
	return segments
}

func ptr(v uint32) *uint32 { return &v }
