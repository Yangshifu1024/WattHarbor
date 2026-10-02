package database

import (
	"context"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"gorm.io/gorm"
)

// 订单冻结口径的取数。快照本身存在 charge_pricing_snapshot，
// 刷卡购买信息存在 card_charge；两者都只按订单主键定位，不重算。

// orderSchemeNames 分页之后一次读取当页快照，避免为每条订单读取当前站点或单独查库。
func OrderSchemeNames(ctx context.Context, db *gorm.DB, rows []OrderView) error {
	ids := make([]uint64, 0, len(rows))
	for i := range rows {
		rows[i].SelectedSchemeName = nil
		rows[i].SelectedPackageName = nil
		ids = append(ids, rows[i].OrderID)
	}
	if len(ids) == 0 {
		return nil
	}
	var snapshots []ChargePricingSnapshotRecord
	if err := db.WithContext(ctx).Select("charge_order_id,pricing_snapshot").Where("charge_order_id IN ?", ids).Find(&snapshots).Error; err != nil {
		return err
	}
	type selectionNames struct{ scheme, packageName *string }
	names := make(map[uint64]selectionNames, len(snapshots))
	for _, snapshot := range snapshots {
		selected := charging.PackageFromSnapshot(snapshot.PricingSnapshot)
		names[snapshot.ChargeOrderID] = selectionNames{charging.SelectedSchemeName(selected), charging.SelectedPackageName(selected)}
	}
	for i := range rows {
		rows[i].SelectedSchemeName = names[rows[i].OrderID].scheme
		rows[i].SelectedPackageName = names[rows[i].OrderID].packageName
	}
	return nil
}

func OrderPackageSnapshot(ctx context.Context, db *gorm.DB, id uint64) (*charging.OrderPackage, error) {
	var snapshot ChargePricingSnapshotRecord
	if err := db.WithContext(ctx).Select("pricing_snapshot").Where("charge_order_id = ?", id).Find(&snapshot).Error; err != nil {
		return nil, err
	}
	out := charging.PackageFromSnapshot(snapshot.PricingSnapshot)
	if out == nil {
		return nil, nil
	}
	var card struct {
		ChargeOrderID uint64
		charging.OrderCardPurchase
	}
	if err := db.WithContext(ctx).Table("card_charge").
		Select("charge_order_id,paid_cents,purchased_minutes,max_minutes").
		Where("charge_order_id = ?", id).Find(&card).Error; err != nil {
		return nil, err
	}
	if card.ChargeOrderID != 0 {
		out.Card = &card.OrderCardPurchase
	}
	return out, nil
}
