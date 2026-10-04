package card

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// OrderCardPurchase 是一次用卡购买的实际扣费与时长。
type OrderCardPurchase struct {
	PaidCents        int64  `json:"paid_cents"`        // 实付金额，单位分
	PurchasedMinutes uint16 `json:"purchased_minutes"` // 实际购买的时长，单位分钟
	MaxMinutes       uint16 `json:"max_minutes"`       // 本单允许的最长充电时长，单位分钟
}

// PurchasedMinutesForOrder 读取 orderID 对应的刷卡已购时长，单位分钟。
// ctx 取消数据库读取；db 可以是事务。无会话返回 0、nil；数据库失败返回错误。
// 本读取不加锁，截止决策必须再使用 LockPurchaseForOrder 获取当前值。
func PurchasedMinutesForOrder(ctx context.Context, db *gorm.DB, orderID uint64) (uint16, error) {
	var row OrderCardPurchase
	err := db.WithContext(ctx).Table("card_charge").Where("charge_order_id=?", orderID).Find(&row).Error
	return row.PurchasedMinutes, err
}

// LockPurchaseForOrder 在调用方 tx 中锁定 orderID 的刷卡购买记录。
// 返回当前已购金额及分钟数；会话不存在返回 gorm.ErrRecordNotFound，其他错误表示读取失败。
// 调用方负责提交或回滚，必须先取此锁再锁订单，与刷卡续费保持相同顺序。
func LockPurchaseForOrder(tx *gorm.DB, orderID uint64) (OrderCardPurchase, error) {
	var row OrderCardPurchase
	err := tx.Table("card_charge").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", orderID).Take(&row).Error
	return row, err
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
