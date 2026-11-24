package marketing

import (
	"context"
	"errors"
	"time"
	marketingdomain "wattharbor/internal/domain/marketing"

	"gorm.io/gorm"
)

var (
	// ErrCouponNotFound 表示券不属于该用户、模板不可用或优惠配置不合法。
	ErrCouponNotFound = errors.New("优惠券不存在或未发放给该账号")
	// ErrCouponExhausted 表示券已使用、已到期或核销时状态不再为 unused。
	ErrCouponExhausted = errors.New("优惠券已过期或已使用")
	// ErrCouponThreshold 表示原价非正或低于券的使用门槛。
	ErrCouponThreshold = errors.New("未达到该优惠券的使用门槛")
	// ErrCouponExceedsFee 定义优惠超额错误；当前优惠计算会封顶到原价，不返回此错误。
	ErrCouponExceedsFee = errors.New("优惠金额不能超过订单金额")
)

// CouponGrant 组合发券记录和模板规则，保留发放、到期及核销信息。
// Status 是 coupon_grant.status 的 GrantStatus；Type 来自 coupon.discount_type 的 DiscountType，
// 两者来自不同表、不同词表，不得互换。
type CouponGrant struct {
	ID                 uint64                      `json:"id" gorm:"column:id"`                                     // 已保存记录主键
	CouponID           uint64                      `json:"coupon_id" gorm:"column:coupon_id"`                       // 模板主键
	Name               string                      `json:"name" gorm:"column:name"`                                 // 业务名称
	GrantID            uint64                      `json:"grant_id" gorm:"column:grant_id"`                         // 发放主键
	Status             marketingdomain.GrantStatus `json:"status" gorm:"column:status"`                             // 业务状态
	ExpiresAt          time.Time                   `json:"expires_at" gorm:"column:expired_at"`                     // 券的 UTC 到期时刻
	MinCharge          int64                       `json:"min_charge_cents" gorm:"column:min_charge_cents"`         // 使用门槛，分
	Type               DiscountType                `json:"discount_type" gorm:"column:discount_type"`               // 优惠类型
	Amount             *int64                      `json:"discount_value_cents" gorm:"column:discount_value_cents"` // 减免分值，可空
	Percent            *float64                    `json:"discount_percent" gorm:"column:discount_percent"`         // 应付比例百分数，80 表示八折；可空
	FreeMinutes        uint32                      `json:"free_minutes" gorm:"column:free_minutes"`                 // 模板免费分钟，不读取金额列
	UsedPaymentOrderID *uint64                     `json:"-" gorm:"column:used_payment_order_id"`                   // 核销时的支付订单 ID，空值表示未核销
}

// TableName 指定优惠券发放表。
func (CouponGrant) TableName() string { return "coupon_grant" }

// CouponRepo 读取用户持券；可用条件和核销由 CouponService 判断。
type CouponRepo struct {
	DB *gorm.DB // 所属中央连接或调用方事务，由进程管理
}

// AvailableCoupons 按到期时间读取本人未用、未过期券。
// JOIN 内的 c.status = 'active' 是 coupon.status 的 CouponStatus 词表字面量，
// 嵌在 JOIN 条件里保留原写法，不拆分查询。
func (s CouponRepo) AvailableCoupons(ctx context.Context, userID uint64) ([]CouponGrant, error) {
	rows := []CouponGrant{}
	err := s.DB.WithContext(ctx).Table("coupon_grant AS g").
		Joins("JOIN coupon AS c ON c.id = g.coupon_id AND c.deleted_at IS NULL AND c.status = 'active'").
		Where("g.user_id = ? AND g.status = ? AND g.deleted_at IS NULL AND g.expired_at > UTC_TIMESTAMP(3)", userID, marketingdomain.GrantUnused).
		Select("g.id, g.coupon_id, g.id AS grant_id, g.status, g.expired_at, c.name, c.discount_type, c.discount_value_cents, c.discount_percent, c.min_charge_cents").
		Order("g.expired_at, g.id").Find(&rows).Error
	return rows, err
}
