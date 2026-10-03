package card

import (
	"context"

	"gorm.io/gorm"
)

// OrderCardPurchase 是一次用卡购买的实际扣费与时长。
type OrderCardPurchase struct {
	PaidCents        int64  `json:"paid_cents"`        // 实付金额，单位分
	PurchasedMinutes uint16 `json:"purchased_minutes"` // 实际购买的时长，单位分钟
	MaxMinutes       uint16 `json:"max_minutes"`       // 本单允许的最长充电时长，单位分钟
}

// OrderCardCharge 是一次用卡购买。ChargeOrderID 为 0 表示该订单没用卡。
type OrderCardCharge struct {
	ChargeOrderID uint64
	OrderCardPurchase
}

// FindOrderCardCharge 返回订单的用卡购买记录。取不到时 ChargeOrderID 为 0，
// 调用方据此判断"这张单没用卡"，而不是当成查询失败。
func FindOrderCardCharge(ctx context.Context, db *gorm.DB, orderID uint64) (OrderCardCharge, error) {
	var card OrderCardCharge
	err := db.WithContext(ctx).Table("card_charge").
		Where("charge_order_id = ?", orderID).Take(&card).Error
	return card, err
}
