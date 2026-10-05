package order

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LockCardChargingInTx 在调用方 tx 中锁定 id 未删除且充电中的原订单，返回完整订单或缺失／存储错误。
// card 必须先锁卡会话，再读取订单，保持截止冻结同一锁序；不改变订单或独立提交。
func LockCardChargingInTx(tx *gorm.DB, id uint64) (ChargeOrderRecord, error) {
	var row ChargeOrderRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='charging' AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}
