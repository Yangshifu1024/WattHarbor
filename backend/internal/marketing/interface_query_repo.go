package marketing

import (
	"context"

	"gorm.io/gorm"
)

// ListUserCoupons 读取启用、未删模板对应的本人券，含已用和到期记录；展示状态由 CouponQueryService 计算。
func ListUserCoupons(ctx context.Context, db *gorm.DB, userID uint64) ([]CouponGrant, error) {
	rows := []CouponGrant{}
	err := db.WithContext(ctx).Table("coupon_grant AS g").
		Joins("JOIN coupon AS c2 ON c2.id = g.coupon_id AND c2.deleted_at IS NULL AND c2.status = 'active'").
		Where("g.user_id = ? AND g.deleted_at IS NULL", userID).
		Select("g.id, g.coupon_id, g.id AS grant_id, g.status, g.expired_at, c2.name, " +
			"c2.discount_type, c2.discount_value_cents, c2.discount_percent, c2.min_charge_cents").
		Order("g.status ASC, g.expired_at ASC, g.id ASC").Find(&rows).Error
	return rows, err
}
