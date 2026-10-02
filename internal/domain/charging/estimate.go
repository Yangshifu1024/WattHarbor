package charging

import "time"

// EstimateCharge 将预计电量均匀分配到请求时长，再调用 Cost 计算报价。
// 报价使用均匀负载假设，最终结算以实际计量为准；设备计费直接返回预付款金额。
func EstimateCharge(rule Rule, energy string, minutes uint16, start time.Time, prepaidCents int64) (Estimate, error) {
	if rule.ID == 0 || !rule.Spec.Mode.Valid() || minutes == 0 || minutes > 600 {
		return Estimate{}, ErrInvalidPricing
	}
	if ValidateSpec(rule.Spec) != nil {
		return Estimate{}, ErrInvalidPricing
	}
	if !energyPattern.MatchString(energy) {
		return Estimate{}, ErrInvalidPricing
	}
	kwh, err := parseEnergy(energy)
	if err != nil {
		return Estimate{}, err
	}
	if !rule.Spec.Mode.ServerBilled() {
		if prepaidCents < 0 || prepaidCents > maxRateCents {
			return Estimate{}, ErrInvalidPricing
		}
		return Estimate{EstimatedKWh: kwh.StringFixed(3), EstimatedMinutes: minutes, Mode: rule.Spec.Mode,
			TotalCents: prepaidCents, PrepaidCents: prepaidCents, ChargeQuantity: minutes}, nil
	}
	wh := kwh.Mul(decimalThousand).IntPart()
	if wh < 1 || wh > 100000 {
		return Estimate{}, ErrInvalidPricing
	}
	usage := Usage{Start: start, End: start.Add(time.Duration(minutes) * time.Minute), EnergyWh: uint64(wh), Channel: rule.Channel}
	base := wh / int64(minutes)
	remainder := wh % int64(minutes)
	for i := 0; i < int(minutes); i++ {
		energy := base
		if int64(i) < remainder {
			energy++
		}
		usage.Samples = append(usage.Samples, Sample{
			Start:    start.Add(time.Duration(i) * time.Minute),
			End:      start.Add(time.Duration(i+1) * time.Minute),
			EnergyWh: uint64(energy),
		})
	}
	fee, err := Cost(rule.Spec, usage)
	if err != nil {
		return Estimate{}, ErrInvalidPricing
	}
	if fee.TotalCents <= 0 {
		return Estimate{}, ErrInvalidPricing
	}
	return Estimate{EstimatedKWh: kwh.StringFixed(3), EstimatedMinutes: minutes, Mode: rule.Spec.Mode, Basis: fee.Basis,
		ElectricCents: fee.ElectricCents, ServiceCents: fee.ServiceCents, TotalCents: fee.TotalCents,
		ChargeMode: 0, ChargeQuantity: minutes}, nil
}
