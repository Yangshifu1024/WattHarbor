package database

import (
	"context"

	"gorm.io/gorm"
)

// 后台订单详情与订单过程视图的读取。订单主查询与站点回填在 order_store 里，
// 这里承接计费单号、分账汇总与事件时间线——它们分散在三个库里，按订单聚到
// 一起只有后台详情这一个场景用到。

// OrderDetailStore 读取订单的计费与分账视图。
type OrderDetailStore struct {
	UserDB, BillingDB *gorm.DB
}

// FirstCalculationNo 返回订单的计费单号，取不到表示尚未计费完成，返回 nil 而非报错。
func (s OrderDetailStore) FirstCalculationNo(ctx context.Context, orderID uint64) (*string, error) {
	var row struct{ CalculationNo *string }
	err := s.BillingDB.WithContext(ctx).Table("fee_receipt").Select("calculation_no").
		Where("charge_order_id = ?", orderID).Limit(1).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	return row.CalculationNo, nil
}

// SettlementsOfOrder 返回订单的全部分账单，并逐张带上参与方明细。
// 参与方金额是结算当时的快照，之后改分账模板不会回溯。
func (s OrderDetailStore) SettlementsOfOrder(ctx context.Context, orderNo string) ([]SettlementView, error) {
	settlements := []SettlementView{}
	if err := s.BillingDB.WithContext(ctx).Table("settlement").
		Where("order_no = ?", orderNo).Order("id").Find(&settlements).Error; err != nil {
		return nil, err
	}
	for i := range settlements {
		settlements[i].Parties = []PartyView{}
		if err := s.BillingDB.WithContext(ctx).Table("settlement_party_amount").
			Where("settlement_id = ?", settlements[i].SettlementID).Order("id").
			Find(&settlements[i].Parties).Error; err != nil {
			return nil, err
		}
	}
	return settlements, nil
}

// OrderExists 确认订单存在且未软删除。时间线接口先确认订单存在，
// 免得对不存在的订单返回一份空时间线。
func OrderExists(ctx context.Context, db *gorm.DB, id uint64) error {
	var row struct{ ID uint64 }
	return db.WithContext(ctx).Table("charge_order").Select("id").
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
}

// ListOrderEvents 返回订单的事件时间线，按发生时间与 ID 排序。
func ListOrderEvents(ctx context.Context, db *gorm.DB, orderID uint64) ([]OrderTimelineRow, error) {
	rows := []OrderTimelineRow{}
	err := db.WithContext(ctx).Table("charge_event_log").
		Where("charge_order_id = ?", orderID).Order("occurred_at,id").Find(&rows).Error
	return rows, err
}

// FindOrderView 按主键取一条订单视图（含联表带出的站点与用户信息）。
func FindOrderView(ctx context.Context, db *gorm.DB, id uint64) (OrderView, error) {
	var row OrderView
	err := OrderBaseQuery(ctx, db).Select(OrderColumns).Where("c.id = ?", id).Take(&row).Error
	return row, err
}
