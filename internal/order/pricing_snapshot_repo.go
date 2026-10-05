package order

import (
	"context"

	"gorm.io/gorm"
)

// 订单冻结的计价快照。管理后台回显的是下单当时的口径，不是当前计费模板，
// 所以读的是 charge_order_pricing 的快照行而不是 pricing_template。
// ListPricingSnapshots 在 ctx 下用 db 批量读取 orderIDs 原快照，返回原数组或读取错误；无写入。
func ListPricingSnapshots(ctx context.Context, db *gorm.DB, orderIDs []uint64) ([]ChargePricingSnapshotRecord, error) {
	rows := []ChargePricingSnapshotRecord{}
	err := db.WithContext(ctx).Select("charge_order_id,pricing_snapshot").
		Where("charge_order_id IN ?", orderIDs).Find(&rows).Error
	return rows, err
}

// FindPricingSnapshot 在 ctx 下用 db 按 orderID 读取原快照，返回原记录或读取错误。
// 沿用 Find 语义，缺行返回零记录及 nil，调用方不得用当前方案替代，无写入。
func FindPricingSnapshot(ctx context.Context, db *gorm.DB, orderID uint64) (ChargePricingSnapshotRecord, error) {
	var row ChargePricingSnapshotRecord
	err := db.WithContext(ctx).Select("pricing_snapshot").
		Where("charge_order_id = ?", orderID).Find(&row).Error
	return row, err
}
