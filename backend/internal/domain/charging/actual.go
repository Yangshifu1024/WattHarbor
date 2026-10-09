package charging

import (
	"time"
)

// MeterSegment 保存连续时间段的电量和功率；相邻段必须首尾相接。
type MeterSegment struct {
	StartedAt time.Time `json:"started_at"` // 分段开始时刻
	EndedAt   time.Time `json:"ended_at"`   // 分段结束时刻
	EnergyWh  uint32    `json:"energy_wh"`  // 本段电量，Wh
	// PeakW 是该段峰值功率，W；最大功率计费使用此值，不由电量推算。
	PeakW uint32 `json:"peak_w,omitempty"`
	// PowerW 是该段实测功率，W；nil 表示缺失，指向 0 表示已知零功率。
	// 实时功率计费要求此值，不能用峰值或电量替代。
	PowerW *uint32 `json:"power_w,omitempty"`
}

// ActualMeter 保存一次充电的实测总量与分段；ReviewRequired 为真时拒绝自动计费。
type ActualMeter struct {
	ReviewRequired bool           `json:"review_required,omitempty"`
	StartedAt      time.Time      `json:"started_at"`
	EndedAt        time.Time      `json:"ended_at"`
	ChargedWh      uint32         `json:"charged_wh"`
	ChargedSeconds uint32         `json:"charged_seconds"`
	Segments       []MeterSegment `json:"segments,omitempty"`
}

// ActualFee 是实际用量的计费结果，金额单位为分。
type ActualFee = Fee

// PriceActual 按 rule 计算实测费用；规则无效返回 ErrInvalidPricing，计量缺失或矛盾返回 ErrMeterReview。
// 电量计费跨越不同费率时必须有分段读数，不按时长分配电量。
func PriceActual(rule Rule, meter ActualMeter) (Fee, error) {
	usage, err := usageFromMeter(rule, meter)
	if err != nil {
		return Fee{}, err
	}
	return Cost(rule.Spec, usage)
}

// usageFromMeter 校验计量记录并转换为 Cost 所需的 Usage。
// 费用封顶与最终结算共用此校验，保持计量有效性及计费结果一致。
func usageFromMeter(rule Rule, meter ActualMeter) (Usage, error) {
	if meter.ReviewRequired {
		return Usage{}, ErrMeterReview
	}
	if ValidateSpec(rule.Spec) != nil || rule.ID == 0 || rule.Version == 0 {
		return Usage{}, ErrInvalidPricing
	}
	if meter.StartedAt.IsZero() || meter.EndedAt.Before(meter.StartedAt) || meter.EndedAt.Sub(meter.StartedAt) > 7*24*time.Hour || time.Duration(meter.ChargedSeconds)*time.Second > meter.EndedAt.Sub(meter.StartedAt)+5*time.Minute {
		return Usage{}, ErrMeterReview
	}
	segments := meter.Segments
	if len(segments) == 0 {
		segments = []MeterSegment{{StartedAt: meter.StartedAt, EndedAt: meter.EndedAt, EnergyWh: meter.ChargedWh}}
	}
	if len(segments) > maxSlices {
		return Usage{}, ErrMeterReview
	}
	usage := Usage{Start: meter.StartedAt, End: meter.EndedAt, EnergyWh: uint64(meter.ChargedWh), Channel: rule.Channel}
	// 费率不变时可直接按总电量计价；费率变化时须有边界读数，不能按时长分摊电量。
	if len(meter.Segments) == 0 && !specIsUniformOver(rule.Spec, meter.StartedAt, meter.EndedAt) {
		return Usage{}, ErrMeterReview
	}
	totalWh := uint64(0)
	cursor := meter.StartedAt
	for _, segment := range segments {
		if !segment.StartedAt.Equal(cursor) || segment.EndedAt.Before(segment.StartedAt) || segment.EndedAt.After(meter.EndedAt) || segment.StartedAt.Equal(segment.EndedAt) && segment.EnergyWh > 0 {
			return Usage{}, ErrMeterReview
		}
		cursor = segment.EndedAt
		totalWh += uint64(segment.EnergyWh)
		power := segment.PeakW
		if rule.Spec.Mode == ModeServerRealtimePower {
			if segment.PowerW == nil {
				return Usage{}, ErrMeterReview
			}
			power = *segment.PowerW
		}
		if rule.Spec.Mode == ModeServerMaxPower && power == 0 && segment.PowerW == nil && meter.ChargedWh > 0 {
			return Usage{}, ErrMeterReview
		}
		usage.Samples = append(usage.Samples, Sample{Start: segment.StartedAt, End: segment.EndedAt,
			EnergyWh: uint64(segment.EnergyWh), PowerW: power, PowerKnown: segment.PowerW != nil})
	}
	if !cursor.Equal(meter.EndedAt) || totalWh != uint64(meter.ChargedWh) {
		return Usage{}, ErrMeterReview
	}
	return usage, nil
}

// StopAtMeter 校验 meter 后判断是否达到 rule 的时长或费用上限。
// 规则或计量无效时返回错误；设备计费返回无需平台停机的计划。
func StopAtMeter(rule Rule, meter ActualMeter) (StopPlan, error) {
	usage, err := usageFromMeter(rule, meter)
	if err != nil {
		return StopPlan{}, err
	}
	elapsed := meter.EndedAt.Sub(meter.StartedAt)
	return DecideStop(rule.Spec, usage, elapsed)
}
