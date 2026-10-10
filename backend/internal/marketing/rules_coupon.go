package marketing

import (
	"errors"
	"time"
)

// 券能否发放、何时到期和如何重放由统一业务规则决定，避免各处理器重复判断。

// ErrCouponGrant 表示模板不可发放、数量达上限、用户状态不符或同请求内容不同。
var ErrCouponGrant = errors.New("优惠券不可发放")

// CouponTemplate 是发放时需要用到的券模板字段。
// Status 对应 coupon.status（券模板启停），与 coupon_grant.status 的 GrantStatus 不同词表。
type CouponTemplate struct {
	TemplateID   uint64       // 券模板主键
	Status       CouponStatus // 模板状态：active 可发放 / disabled 不可发放
	TotalQuota   uint32       // 发放总量上限，0 表示不限量
	PerUserQuota uint32       // 每人发放上限，必须大于 0
	ValidHours   uint32       // 领取后的有效小时数
	StartAt      *time.Time   // 生效开始时间；nil 表示立即生效
	EndAt        *time.Time   // 生效结束时间；nil 表示长期有效
}

// CouponUsage 是某个券模板当前的发放量。
type CouponUsage struct {
	TotalGranted int64 // 已发放总份数
	UserGranted  int64 // 该用户已获得的份数
}

// CheckGrantable 检查模板启用、时间有效，且总发放份数和单人份数均未达上限。
// 只接受 coupon.status 词表内的 CouponActive；其他取值（含未知值）一律不可发放。
func CheckGrantable(now time.Time, coupon CouponTemplate, usage CouponUsage) error {
	if coupon.Status != CouponActive {
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

// CouponGrantReceipt 保存请求号对应的用户、券模板和首次发放 ID，供重试比对。
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
