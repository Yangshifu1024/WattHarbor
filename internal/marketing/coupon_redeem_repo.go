package marketing

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// loadGrant 以 ctx、db 按原 userID／grantID 读取启用模板的权益；locked 时在调用方事务取得写锁。
// 返回所属行或无券／存储错误；业务有效期、优惠及重放由 CouponService 仲裁，不自行提交。
func loadGrant(ctx context.Context, db *gorm.DB, userID, grantID uint64, locked bool) (CouponGrant, error) {
	grant := CouponGrant{}
	if locked {
		// 核销也遵循模板→发放锁序；不能先锁发放再等待活动已持有的模板。
		var identity struct {
			CouponID uint64 `gorm:"column:coupon_id"` /* 原不可变模板身份 */
		}
		err := db.WithContext(ctx).Table("coupon_grant").Select("coupon_id").Where("id=? AND user_id=? AND deleted_at IS NULL", grantID, userID).Take(&identity).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return grant, ErrCouponNotFound
		}
		if err != nil {
			return grant, err
		}
		var coupon struct {
			ID uint64 `gorm:"column:id"` /* 原模板 */
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

// redemptionReceipt 是已核销权益对应的原支付凭据投影。
type redemptionReceipt struct {
	CouponID      uint64 `gorm:"column:coupon_id"`      // 原券模板主键
	UserID        uint64 `gorm:"column:user_id"`        // 原支付用户
	PaymentOrder  uint64 `gorm:"column:biz_id"`         // 原支付主键
	DiscountCents int64  `gorm:"column:discount_cents"` // 首次核销整分优惠
	Reference     string `gorm:"column:order_no"`       // 首次支付业务号
}

// findRedemption 在 tx 锁定权益后当前读取 paymentID 的原charge核销，不自行提交或修改。
func findRedemption(tx *gorm.DB, paymentID uint64) (redemptionReceipt, error) {
	var row redemptionReceipt
	err := tx.Table("coupon_redemption").Clauses(clause.Locking{Strength: "UPDATE"}).Where("biz_type='charge' AND biz_id=?", paymentID).Take(&row).Error
	return row, err
}

// saveRedemption 在 tx 中以 ref 原身份更新 grant 为已用并追加 reference 对应凭据；任一失败由调用方回滚。
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
