package charging

import (
	"chargepilot/internal/domain/devices"
	"time"
)

// maxSamples 限制单次计费的心跳条数，防止异常上报导致无界计算；与网关取数上限一致。
const maxSamples = 10080

// clockSkewTolerance 允许设备时钟与充电开始时刻的最大偏差。
// 超出即判定证据自相矛盾，整段拒绝而不是取其一。
const clockSkewTolerance = 5 * time.Second

// powerJoinWindow 相邻心跳间隔不超过该值时，才用前次功率描述该区间。
// 更长的间隔保留为证据缺失，不从电量反推功率。
const powerJoinWindow = 60 * time.Second

// MeasuredSegments 按设备累计读数生成计费分段，不用接收时间分摊电量。
// start 来自中央启动回执，span.StartedAt 来自设备结束帧，两者允许存在时钟偏差。
// 起充前容差内且没有新增电量的读数可跳过，仍保留其功率供后续区间使用。
// 时长、时钟、精度、读数顺序或电量合计不符时返回 nil，交由计量复核。
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
				// 只跳过起充前容差内、且没有新增电量的读数；不移动分段起点或累计电量。
				// 功率仍可供后续区间使用。超出容差、起充后时间回退或电量增加都须拒绝。
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
