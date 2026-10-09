package marketing

import (
	"errors"
	"time"
)

// 券能否发放、何时到期和如何重放由统一业务规则决定，避免各处理器重复判断。

// ErrCouponGrant 表示券不可用、额度已尽、用户状态不符或重放内容不同；直接重试不能解决。
var ErrCouponGrant = errors.New("优惠券不可发放")

// CouponTemplate 是发放时需要用到的券模板字段。
type CouponTemplate struct {
	TemplateID   uint64     // 券模板主键
	Status       string     // 模板状态：active 可发放 / disabled 不可发放
	TotalQuota   uint32     // 发放总量上限，0 表示不限量
	PerUserQuota uint32     // 每人发放上限，必须大于 0
	ValidHours   uint32     // 领取后的有效小时数
	StartAt      *time.Time // 生效开始时间；nil 表示立即生效
	EndAt        *time.Time // 生效结束时间；nil 表示长期有效
}

// CouponUsage 是某个券模板当前的发放量。
type CouponUsage struct {
	TotalGranted int64 // 已发放总份数
	UserGranted  int64 // 该用户已获得的份数
}

// CheckGrantable 检查模板启用、当前有效、总额未尽和单人未达上限，时间由调用方提供。
func CheckGrantable(now time.Time, coupon CouponTemplate, usage CouponUsage) error {
	if coupon.Status != "active" {
		return ErrCouponGrant
	}
	if coupon.StartAt != nil && now.Before(*coupon.StartAt) {
		return ErrCouponGrant
	}
	if coupon.EndAt != nil && !now.Before(*coupon.EndAt) {
		return ErrCouponGrant
	}
	if coupon.TotalQuota > 0 && usage.TotalGranted >= int64(coupon.TotalQuota) {
		return ErrCouponGrant
	}
	if usage.UserGranted >= int64(coupon.PerUserQuota) {
		return ErrCouponGrant
	}
	return nil
}

// GrantExpiry 从发券时间起计算 valid_hours 小时，若模板结束更早则以模板结束为上限。
func GrantExpiry(now time.Time, coupon CouponTemplate) time.Time {
	expires := now.Add(time.Duration(coupon.ValidHours) * time.Hour)
	if coupon.EndAt != nil && coupon.EndAt.Before(expires) {
		return *coupon.EndAt
	}
	return expires
}

// CouponGrantReceipt 是某次发放的受理凭据，request_id 是它的幂等键。
type CouponGrantReceipt struct {
	RequestID     string // 客户端请求号
	CouponID      uint64 // 券模板 ID
	UserID        uint64 // 领取用户 ID
	CouponGrantID uint64 // 发放记录主键
}

// ReplayGrant 同一请求号且券、用户一致时返回首次发放；内容不同返回冲突。
func ReplayGrant(receipt CouponGrantReceipt, requestID string, couponID, userID uint64) (uint64, error) {
	if receipt.RequestID != requestID {
		return 0, ErrCouponGrant
	}
	if receipt.CouponID != couponID || receipt.UserID != userID {
		return 0, ErrCouponGrant
	}
	return receipt.CouponGrantID, nil
}
