package order

import (
	"context"
	"gorm.io/gorm"
	"time"
)

// OrderTimelineRow 是所属订单事件的公开只读投影，保留原发生时间排序。
type OrderTimelineRow struct {
	EventID string    `json:"event_id"`                     /* 原事件身份 */
	At      time.Time `json:"at" gorm:"column:occurred_at"` /* 发生UTC时刻 */
	Event   string    `json:"event"`                        /* 原事件类型 */
	Actor   string    `json:"actor"`                        /* 原执行来源 */
	Detail  string    `json:"detail"`                       /* 原业务说明 */
}

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
func ListOrderEvents(ctx context.Context, db *gorm.DB, orderID uint64) ([]OrderTimelineRow, error) {
	rows := []OrderTimelineRow{}
	err := db.WithContext(ctx).Table("charge_event_log").
		Where("charge_order_id = ?", orderID).Order("occurred_at,id").Find(&rows).Error
	return rows, err
}

// FindOrderView 按主键取一条订单视图（含联表带出的站点与用户信息）。
// ctx 取消 db 查询，id 为已授权订单；返回原投影或缺行／存储错误，不组合实时网关事实。
func FindOrderView(ctx context.Context, db *gorm.DB, id uint64) (OrderView, error) {
	var row OrderView
	err := orderBaseQuery(ctx, db).Select(orderColumns).Where("c.id = ?", id).Take(&row).Error
	return row, err
}
