package card

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// LockChargeForOrder 只对 card_charge 取行锁参与加锁顺序，不读任何字段。
// 这样起充回执与刷卡会话共用同一把锁，避免两处同时改同一笔卡的占用端口。
func LockChargeForOrder(tx *gorm.DB, chargeOrderID uint64) error {
	var locked struct{ ID uint64 }
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("card_charge").
		Where("charge_order_id=?", chargeOrderID).Find(&locked).Error
}

// ConfirmStartOperation 把等待确认的刷卡起充操作标记为已确认。
// 只有刷卡起充才有这张表的行；扫码与余额起充影响 0 行是正常结果。
// 返回值供调用方按需判断，调用方不得把 0 行当冲突——判据是 err，不是 RowsAffected。
func ConfirmStartOperation(tx *gorm.DB, chargeOrderID uint64, at time.Time) (int64, error) {
	res := tx.Table("card_operation").
		Where("charge_order_id=? AND kind='start' AND status='confirming'", chargeOrderID).
		Updates(map[string]any{"status": "confirmed", "confirmed_at": at})
	return res.RowsAffected, res.Error
}

// FailStartOperation 把该订单的刷卡起充操作标记为失败。
func FailStartOperation(tx *gorm.DB, chargeOrderID uint64, reason string, at time.Time) error {
	return tx.Table("card_operation").Where("charge_order_id=? AND kind='start'", chargeOrderID).
		Updates(map[string]any{"status": "failed", "failure_reason": reason, "confirmed_at": at}).Error
}

// ClearActivePort 释放卡在这笔订单上占用的端口。
func ClearActivePort(tx *gorm.DB, chargeOrderID uint64) error {
	return tx.Table("card_charge").Where("charge_order_id=?", chargeOrderID).
		Update("active_port", nil).Error
}
