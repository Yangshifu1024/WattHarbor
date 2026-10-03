package marketing

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

var (
	ErrCouponNotFound   = errors.New("优惠券不存在或未发放给该账号")
	ErrCouponExhausted  = errors.New("优惠券已过期或已使用")
	ErrCouponThreshold  = errors.New("未达到该优惠券的使用门槛")
	ErrCouponExceedsFee = errors.New("优惠金额不能超过订单金额")
)

type CouponGrant struct {
	ID          uint64    `json:"id" gorm:"column:id"`
	CouponID    uint64    `json:"coupon_id" gorm:"column:coupon_id"`
	Name        string    `json:"name" gorm:"column:name"`
	GrantID     uint64    `json:"grant_id" gorm:"column:grant_id"`
	Status      string    `json:"status" gorm:"column:status"`
	ExpiresAt   time.Time `json:"expires_at" gorm:"column:expired_at"`
	MinCharge   int64     `json:"min_charge_cents" gorm:"column:min_charge_cents"`
	Type        string    `json:"discount_type" gorm:"column:discount_type"`
	Amount      *int64    `json:"discount_value_cents" gorm:"column:discount_value_cents"`
	Percent     *float64  `json:"discount_percent" gorm:"column:discount_percent"`
	FreeMinutes uint32    `json:"free_minutes" gorm:"-"`
}

func (CouponGrant) TableName() string { return "coupon_grant" }

// CouponStore 负责把客户的券解析出来并核销到一笔充电上。
type CouponStore struct{ DB *gorm.DB }

// AvailableCoupons 列出客户当前还可以用的券。
func (s CouponStore) AvailableCoupons(ctx context.Context, userID uint64) ([]CouponGrant, error) {
	rows := []CouponGrant{}
	err := s.DB.WithContext(ctx).Table("coupon_grant AS g").
		Joins("JOIN coupon AS c ON c.id = g.coupon_id AND c.deleted_at IS NULL AND c.status = 'active'").
		Where("g.user_id = ? AND g.status = 'unused' AND g.deleted_at IS NULL AND g.expired_at > UTC_TIMESTAMP(3)", userID).
		Select("g.id, g.coupon_id, g.id AS grant_id, g.status, g.expired_at, c.name, c.discount_type, c.discount_value_cents, c.discount_percent, c.min_charge_cents").
		Order("g.expired_at, g.id").Find(&rows).Error
	return rows, err
}

// Quote 只读计算优惠券减免额，不核销券，供支付前预览。
func (s CouponStore) Quote(ctx context.Context, userID, grantID uint64, totalCents int64) (int64, error) {
	grant, err := s.load(ctx, userID, grantID)
	if err != nil {
		return 0, err
	}
	return discountFor(grant, totalCents)
}

// discountFor 应用优惠规则，减免额不超过本次费用，保证应付金额非负。
