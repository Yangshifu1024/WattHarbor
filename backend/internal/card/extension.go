package card

import "wattharbor/internal/domain/charging"

// CanExtend 判断同卡是否可续购正价时长套餐：没有待确认扣款、余额足够，且加时不超过累计上限。
func CanExtend(card, orderCard uint64, p charging.Package, purchased, max uint16, balance int64, pending bool) bool {
	return card == orderCard && p.Mode == "duration" && p.PriceCents > 0 && p.Minutes > 0 && !pending && balance >= p.PriceCents && uint32(purchased)+uint32(p.Minutes) <= uint32(max)
}
