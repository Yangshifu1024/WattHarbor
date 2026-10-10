package order

import (
	orderdomain "chargepilot/internal/domain/order"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LockCardChargingInTx 在调用方事务中锁定 id 未删除且充电中的订单，返回完整订单，缺失时返回错误。
// card 必须先锁卡会话，再读取订单，保持截止冻结同一锁序；不改变订单或独立提交。
func LockCardChargingInTx(tx *gorm.DB, id uint64) (ChargeOrderRecord, error) {
	var row ChargeOrderRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status=? AND deleted_at IS NULL", id, orderdomain.StatusCharging).Take(&row).Error
	return row, err
}
