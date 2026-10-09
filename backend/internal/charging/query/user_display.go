package query

import (
	charging "chargepilot/internal/domain/charging"
	"fmt"
)

// tariffLines 按 s 原冻结方案与 offer 原套餐生成资费文案，返回数组；不读取当前规则或执行计费。
func tariffLines(s charging.Scheme, offer *charging.Offer) []string {
	lines := []string{}
	if offer == nil {
		return lines
	}
	if offer.Mode == "duration" {
		return []string{fmt.Sprintf("套餐 ¥%.2f / %d分钟，按实际完整分钟结算", float64(offer.PriceCents)/100, offer.DurationMinutes)}
	}
	if offer.Mode == "energy" && s.Energy != nil {
		return []string{fmt.Sprintf("电费 ¥%.2f / 度 · 服务费 ¥%.2f / 度", float64(s.Energy.ElectricCents)/100, float64(s.Energy.ServiceCents)/100)}
	}
	if s.Amount == nil {
		return lines
	}
	begin := 0
	clock := func(n int) string { return fmt.Sprintf("%02d:%02d", n/60, n%60) }
	for _, p := range s.Amount.Periods {
		span := clock(begin) + "～" + clock(p.EndMinute)
		if s.Amount.Algorithm == charging.ModeServerEnergy {
			lines = append(lines, fmt.Sprintf("%s · 电费 ¥%.2f / 度 · 服务费 ¥%.2f / 度", span, float64(p.ElectricCents)/100, float64(p.ServiceCents)/100))
		} else {
			low := 0
			for _, tier := range p.Tiers {
				lines = append(lines, fmt.Sprintf("%s · %d～%d W · 电费 ¥%.2f / 小时 · 服务费 ¥%.2f / 小时", span, low, tier.MaxWatts, float64(tier.ElectricCents)/100, float64(tier.ServiceCents)/100))
				low = tier.MaxWatts + 1
			}
		}
		begin = p.EndMinute
	}
	return lines
}
