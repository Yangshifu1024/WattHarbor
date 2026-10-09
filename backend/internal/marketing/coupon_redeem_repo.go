package marketing

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// loadGrant 按用户和发放 ID 读取启用模板的券，需要核销时在调用方事务中加锁。
// 有效期、优惠和重放由 CouponService 判断。
func loadGrant(ctx context.Context, db *gorm.DB, userID, grantID uint64, locked bool) (CouponGrant, error) {
	grant := CouponGrant{}
	if locked {
		// 核销也遵循模板→发放锁序；不能先锁发放再等待活动已持有的模板。
		var identity struct {
			CouponID uint64 `gorm:"column:coupon_id"` /* 不可变模板 ID */
		}
		err := db.WithContext(ctx).Table("coupon_grant").Select("coupon_id").Where("id=? AND user_id=? AND deleted_at IS NULL", grantID, userID).Take(&identity).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return grant, ErrCouponNotFound
		}
		if err != nil {
			return grant, err
		}
		var coupon struct {
			ID uint64 `gorm:"column:id"` /* 模板 */
		}
		err = db.WithContext(ctx).Table("coupon").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", identity.CouponID).Take(&coupon).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return grant, ErrCouponNotFound
		}
		if err != nil {
			return grant, err
		}
	}
	query := db.WithContext(ctx).Table("coupon_grant AS g").
		Joins("JOIN coupon AS c ON c.id = g.coupon_id AND c.deleted_at IS NULL AND c.status = 'active'").
		Where("g.id = ? AND g.user_id = ? AND g.deleted_at IS NULL", grantID, userID).
		Select("g.id, g.coupon_id, g.id AS grant_id, g.status, g.expired_at, g.used_payment_order_id, c.name, c.discount_type, c.discount_value_cents, c.discount_percent, c.min_charge_cents, COALESCE(c.free_minutes,0) AS free_minutes")
	if locked {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Take(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return grant, ErrCouponNotFound
	}
	return grant, err
}

// redemptionReceipt 保存首次核销的支付订单、用户和优惠金额，供重试比对。
type redemptionReceipt struct {
	CouponID      uint64 `gorm:"column:coupon_id"`      // 券模板主键
	UserID        uint64 `gorm:"column:user_id"`        // 支付用户
	PaymentOrder  uint64 `gorm:"column:biz_id"`         // 支付主键
	DiscountCents int64  `gorm:"column:discount_cents"` // 首次核销整分优惠
	Reference     string `gorm:"column:order_no"`       // 首次支付业务号
}

// findRedemption 在券锁内用当前读查询支付对应的充电核销记录。
func findRedemption(tx *gorm.DB, paymentID uint64) (redemptionReceipt, error) {
	var row redemptionReceipt
	err := tx.Table("coupon_redemption").Clauses(clause.Locking{Strength: "UPDATE"}).Where("biz_type='charge' AND biz_id=?", paymentID).Take(&row).Error
	return row, err
}

// saveRedemption 在事务中将券标为已用并保存核销记录，失败须整体回滚。
func saveRedemption(tx *gorm.DB, grant CouponGrant, ref RedeemRef, reference string) error {
	updated := tx.Table("coupon_grant").Where("id=? AND user_id=? AND status='unused'", ref.CouponGrantID, ref.UserID).
		Updates(map[string]any{"status": "used", "used_payment_order_id": ref.PaymentOrder, "used_at": time.Now().UTC()})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrCouponExhausted
	}
	return tx.Table("coupon_redemption").Create(map[string]any{"coupon_id": grant.CouponID, "user_id": ref.UserID, "biz_type": "charge", "biz_id": ref.PaymentOrder, "discount_cents": ref.DiscountCents, "order_no": reference}).Error
}
