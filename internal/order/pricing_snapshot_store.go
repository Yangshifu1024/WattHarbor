package order

import (
	"context"

	"gorm.io/gorm"
)

// 订单冻结的计价快照。管理后台回显的是下单当时的口径，不是当前计费模板，
// 所以读的是 charge_order_pricing 的快照行而不是 pricing_template。
func ListPricingSnapshots(ctx context.Context, db *gorm.DB, orderIDs []uint64) ([]ChargePricingSnapshotRecord, error) {
	rows := []ChargePricingSnapshotRecord{}
	err := db.WithContext(ctx).Select("charge_order_id,pricing_snapshot").
		Where("charge_order_id IN ?", orderIDs).Find(&rows).Error
	return rows, err
}
func FindPricingSnapshot(ctx context.Context, db *gorm.DB, orderID uint64) (ChargePricingSnapshotRecord, error) {
	var row ChargePricingSnapshotRecord
	err := db.WithContext(ctx).Select("pricing_snapshot").
		Where("charge_order_id = ?", orderID).Find(&row).Error
	return row, err
}
