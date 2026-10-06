package card

import "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"

// CanExtend 按 card/orderCard 原身份、p 冻结套餐、purchased/max 分钟数、balance 可花费分值和 pending 未确认状态判断能否续购。
// 返回全部条件同时满足的标志；仅支持同卡正价时长套餐，不超累计上限，无 IO 或修改。
func CanExtend(card, orderCard uint64, p charging.Package, purchased, max uint16, balance int64, pending bool) bool {
	return card == orderCard && p.Mode == "duration" && p.PriceCents > 0 && p.Minutes > 0 && !pending && balance >= p.PriceCents && uint32(purchased)+uint32(p.Minutes) <= uint32(max)
}
