package card

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// LockChargeForOrder 在调用方事务中锁定刷卡会话，供起充回执与刷卡操作共用锁序。
// 没有刷卡会话时无需处理。
func LockChargeForOrder(tx *gorm.DB, chargeOrderID uint64) error {
	var locked struct {
		ID uint64 /* 锁查询占位查询结果，不用于业务判断 */
	}
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("card_charge").
		Where("charge_order_id=?", chargeOrderID).Find(&locked).Error
}

// ConfirmStartOperation 确认等待中的刷卡起充操作，at 为设备确认的 UTC 时刻。
// 扫码或余额起充没有对应行，影响 0 行是正常结果；调用方只按错误判断失败。
func ConfirmStartOperation(tx *gorm.DB, chargeOrderID uint64, at time.Time) (int64, error) {
	res := tx.Table("card_operation").
		Where("charge_order_id=? AND kind='start' AND status='confirming'", chargeOrderID).
		Updates(map[string]any{"status": "confirmed", "confirmed_at": at})
	return res.RowsAffected, res.Error
}

// FailStartOperation 记录刷卡起充失败原因与 UTC 时刻，与退款、端口释放同事务。
func FailStartOperation(tx *gorm.DB, chargeOrderID uint64, reason string, at time.Time) error {
	return tx.Table("card_operation").Where("charge_order_id=? AND kind='start'", chargeOrderID).
		Updates(map[string]any{"status": "failed", "failure_reason": reason, "confirmed_at": at}).Error
}

// ClearActivePort 在已锁会话的事务中释放订单占用的卡端口；没有会话时无需处理。
func ClearActivePort(tx *gorm.DB, chargeOrderID uint64) error {
	return tx.Table("card_charge").Where("charge_order_id=?", chargeOrderID).
		Update("active_port", nil).Error
}
