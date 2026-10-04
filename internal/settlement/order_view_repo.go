package settlement

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	orderstore "github.com/ChargePilot2026/charge-pilot/internal/order"

	"gorm.io/gorm"
)

// 后台订单详情所需的只读投影，订单基础模型及主查询由 order 持有，退款由 settlement 持有。

// OrderExists 确认订单存在且未软删除。时间线接口先确认订单存在，
// 免得对不存在的订单返回一份空时间线。
// ctx 取消 db 查询，id 为订单主键；返回 nil 或缺行／存储错误，不修改状态，授权由调用方负责。
func OrderExists(ctx context.Context, db *gorm.DB, id uint64) error {
	var row struct {
		ID uint64 // 未删除原订单主键
	}
	return db.WithContext(ctx).Table("charge_order").Select("id").
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
}

// ListOrderEvents 返回订单的事件时间线，按发生时间与 ID 排序。
// ctx 取消 db 查询，orderID 为已授权订单；无事件返回空数组，存储失败返回 error，不写入事件。
func ListOrderEvents(ctx context.Context, db *gorm.DB, orderID uint64) ([]ops.OrderTimelineRow, error) {
	rows := []ops.OrderTimelineRow{}
	err := db.WithContext(ctx).Table("charge_event_log").
		Where("charge_order_id = ?", orderID).Order("occurred_at,id").Find(&rows).Error
	return rows, err
}

// FindOrderView 按主键取一条订单视图（含联表带出的站点与用户信息）。
// ctx 取消 db 查询，id 为已授权订单；返回原投影或缺行／存储错误，不组合实时网关事实。
func FindOrderView(ctx context.Context, db *gorm.DB, id uint64) (orderstore.OrderView, error) {
	var row orderstore.OrderView
	err := orderstore.OrderBaseQuery(ctx, db).Select(orderstore.OrderColumns).Where("c.id = ?", id).Take(&row).Error
	return row, err
}

// ListOrderRefunds 返回订单产生的退款记录，按 ID 倒序。
// ctx 取消 db 查询，orderID 为已授权原订单；无记录返回空数组，读取失败返回 error，不执行退款。
func ListOrderRefunds(ctx context.Context, db *gorm.DB, orderID uint64) ([]map[string]any, error) {
	rows := []map[string]any{}
	err := db.WithContext(ctx).Table("refund_record").
		Where("biz_type = 'charge' AND biz_id = ? AND deleted_at IS NULL", orderID).
		Select("refund_no, refund_cents, status, reason, created_at").Order("id DESC").Find(&rows).Error
	return rows, err
}
