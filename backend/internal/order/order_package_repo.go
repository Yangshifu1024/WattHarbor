package order

import (
	"context"

	charging "chargepilot/internal/domain/charging"
	"gorm.io/gorm"
)

// 订单冻结计价规则的取数。快照本身存在 charge_order_pricing。
// 刷卡购买信息存在 card_charge；两者都只按订单主键定位，不重算。

// OrderSchemeNames 用 db 按 rows 当页订单一次读取快照，原位回填冻结方案与套餐名称。
// 空页无查询；缺失或无效快照保持 nil，读取错误返回 error，不改数据库或读取当前方案。
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
	// selectionNames 缓存每笔订单冻结快照中的方案名与套餐名，供当页回填；缺失名称保持空值，不读取当前发布规则补造。
	type selectionNames struct {
		scheme, packageName *string /* 冻结方案名与套餐名，缺失各自为空 */
	}
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

// OrderPackageSnapshot 读取订单冻结套餐并补充累计刷卡购买信息。
// 快照缺失或无效时返回 nil、nil，不读取当前价格或重算金额。
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
		ChargeOrderID              uint64 // 卡订单编号，零表示无关联
		charging.OrderCardPurchase        // 累计实付金额（分）、购买分钟与冻结时长上限
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
