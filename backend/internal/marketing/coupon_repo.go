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

// CouponGrant 组合发券记录和模板规则，保留发放、到期及核销信息。
type CouponGrant struct {
	ID                 uint64    `json:"id" gorm:"column:id"`                                     // 已保存记录主键
	CouponID           uint64    `json:"coupon_id" gorm:"column:coupon_id"`                       // 模板主键
	Name               string    `json:"name" gorm:"column:name"`                                 // 业务名称
	GrantID            uint64    `json:"grant_id" gorm:"column:grant_id"`                         // 发放主键
	Status             string    `json:"status" gorm:"column:status"`                             // 业务状态
	ExpiresAt          time.Time `json:"expires_at" gorm:"column:expired_at"`                     // 权益 UTC 截止时刻
	MinCharge          int64     `json:"min_charge_cents" gorm:"column:min_charge_cents"`         // 使用门槛，分
	Type               string    `json:"discount_type" gorm:"column:discount_type"`               // 优惠类型
	Amount             *int64    `json:"discount_value_cents" gorm:"column:discount_value_cents"` // 减免分值，可空
	Percent            *float64  `json:"discount_percent" gorm:"column:discount_percent"`         // 优惠百分比，可空
	FreeMinutes        uint32    `json:"free_minutes" gorm:"column:free_minutes"`                 // 模板免费分钟，不读取金额列
	UsedPaymentOrderID *uint64   `json:"-" gorm:"column:used_payment_order_id"`                   // 已用支付身份，空值未核销
}

// TableName 指定优惠券发放表。
func (CouponGrant) TableName() string { return "coupon_grant" }

// CouponRepo 读取用户持券；可用条件和核销由 CouponService 判断。
type CouponRepo struct {
	DB *gorm.DB // 所属中央连接或调用方事务，由进程管理
}

// AvailableCoupons 按到期时间读取本人未用、未过期券。
func (s CouponRepo) AvailableCoupons(ctx context.Context, userID uint64) ([]CouponGrant, error) {
	rows := []CouponGrant{}
	err := s.DB.WithContext(ctx).Table("coupon_grant AS g").
		Joins("JOIN coupon AS c ON c.id = g.coupon_id AND c.deleted_at IS NULL AND c.status = 'active'").
		Where("g.user_id = ? AND g.status = 'unused' AND g.deleted_at IS NULL AND g.expired_at > UTC_TIMESTAMP(3)", userID).
		Select("g.id, g.coupon_id, g.id AS grant_id, g.status, g.expired_at, c.name, c.discount_type, c.discount_value_cents, c.discount_percent, c.min_charge_cents").
		Order("g.expired_at, g.id").Find(&rows).Error
	return rows, err
}
