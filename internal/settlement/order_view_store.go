package settlement

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"

	"gorm.io/gorm"
)

// 后台订单详情与订单过程视图的读取。订单主查询与站点回填在 order_store 里，
// 这里承接计费单号、分账汇总与事件时间线——它们分散在三个库里，按订单聚到
// 一起只有后台详情这一个场景用到。

// OrderDetailStore 读取订单的计费与分账视图。
type OrderDetailStore struct {
	UserDB, BillingDB *gorm.DB
}

// SettlementsOfOrder 返回订单的全部分账单，并逐张带上参与方明细。

// OrderExists 确认订单存在且未软删除。时间线接口先确认订单存在，
// 免得对不存在的订单返回一份空时间线。
func OrderExists(ctx context.Context, db *gorm.DB, id uint64) error {
	var row struct{ ID uint64 }
	return db.WithContext(ctx).Table("charge_order").Select("id").
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
}

// ListOrderEvents 返回订单的事件时间线，按发生时间与 ID 排序。
func ListOrderEvents(ctx context.Context, db *gorm.DB, orderID uint64) ([]ops.OrderTimelineRow, error) {
	rows := []ops.OrderTimelineRow{}
	err := db.WithContext(ctx).Table("charge_event_log").
		Where("charge_order_id = ?", orderID).Order("occurred_at,id").Find(&rows).Error
	return rows, err
}

// FindOrderView 按主键取一条订单视图（含联表带出的站点与用户信息）。
func FindOrderView(ctx context.Context, db *gorm.DB, id uint64) (orderstore.OrderView, error) {
	var row orderstore.OrderView
	err := orderstore.OrderBaseQuery(ctx, db).Select(orderstore.OrderColumns).Where("c.id = ?", id).Take(&row).Error
	return row, err
}
