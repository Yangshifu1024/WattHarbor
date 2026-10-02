package money

import (
	"errors"
	"time"
)

// 优惠券发放的判定与计算。发放资格、有效期上限、幂等重放都是业务规则：
// 它们决定"这张券能不能发、什么时候过期、重复提交怎么办"，
// 与存储和传输无关，因此不在处理器里各判一次。

// ErrCouponGrant 表示本次发放不成立：券不可用、额度已尽、用户状态不对或重放内容不符。
// 这类拒绝不是故障，重试没有意义。
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

// CheckGrantable 判断此刻能否给该用户发这张券。
// 四个条件缺一不可：模板启用、落在生效窗口内、总量未发完、该用户未达上限。
// now 由调用方传入，便于测试与业务日界统一。
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

// GrantExpiry 计算发放后的到期时间。
// 有效期从发放时刻起算 valid_hours；若券本身带结束时间且更早，则以结束时间封顶——
// 券的有效期不得超出它自身的可用窗口。
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

// ReplayGrant 判定一次重复提交能否沿用首次发放。
//
// 同一请求号必须指向同一个券、同一个用户：内容一致就返回首次凭据，
// 券或用户变了则判冲突——绝不能用同一个请求号把 A 券的凭据发给 B 用户。
func ReplayGrant(receipt CouponGrantReceipt, requestID string, couponID, userID uint64) (uint64, error) {
	if receipt.RequestID != requestID {
		return 0, ErrCouponGrant
	}
	if receipt.CouponID != couponID || receipt.UserID != userID {
		return 0, ErrCouponGrant
	}
	return receipt.CouponGrantID, nil
}
