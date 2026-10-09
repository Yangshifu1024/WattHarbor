package marketing

import "github.com/shopspring/decimal"

// discountFor 按原价计算优惠，单位分；减免最多为原价，折扣先算应付额再相减。
// 时长券目前使用固定零费率，优惠金额为零。
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
		// 原价乘应付比例，用十进制计算；
		// 应付金额四舍五入到分，再从原价中扣除。
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

// serviceRatePerMinute 当前固定为 0，时长券尚未按套餐服务费折算优惠金额。
const serviceRatePerMinute = 0
