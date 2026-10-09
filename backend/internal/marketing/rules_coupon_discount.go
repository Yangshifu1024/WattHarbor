package marketing

import "github.com/shopspring/decimal"

// discountFor 按原价计算优惠，金额单位分；减免不超过原价，十进制折扣仅舍入一次。
// 时长券沿用每分钟服务费缺失时的默认值。
func discountFor(grant CouponGrant, totalCents int64) (int64, error) {
	if totalCents <= 0 || grant.MinCharge > totalCents {
		return 0, ErrCouponThreshold
	}
	var discount int64
	switch grant.Type {
	case "amount":
		if grant.Amount == nil || *grant.Amount <= 0 {
			return 0, ErrCouponNotFound
		}
		discount = *grant.Amount
	case "percentage":
		if grant.Percent == nil || *grant.Percent <= 0 || *grant.Percent >= 100 {
			return 0, ErrCouponNotFound
		}
		// 用十进制运算保证百分比精确；
		// 结果只四舍五入一次（half up）到整分。
		keep := decimal.NewFromInt(totalCents).Mul(decimal.NewFromFloat(*grant.Percent)).Div(decimal.NewFromInt(100)).Round(0).IntPart()
		discount = totalCents - keep
	case "time_free":
		if grant.FreeMinutes == 0 {
			return 0, ErrCouponNotFound
		}
		discount = int64(grant.FreeMinutes) * 60 * serviceRatePerMinute
	default:
		return 0, ErrCouponNotFound
	}
	if discount > totalCents {
		discount = totalCents
	}
	if discount < 0 {
		discount = 0
	}
	return discount, nil
}

// serviceRatePerMinute 是规则未指定每分钟服务费时，计算时长券优惠使用的默认值。
const serviceRatePerMinute = 0
