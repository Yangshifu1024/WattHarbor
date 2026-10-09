package order

import (
	"context"
	"gorm.io/gorm"
	"time"
)

// OrderTimelineRow 保存订单事件的接口字段，按发生时间排序。
type OrderTimelineRow struct {
	EventID string    `json:"event_id"`                     /* 事件编号 */
	At      time.Time `json:"at" gorm:"column:occurred_at"` /* 发生UTC时刻 */
	Event   string    `json:"event"`                        /* 事件类型 */
	Actor   string    `json:"actor"`                        /* 执行来源 */
	Detail  string    `json:"detail"`                       /* 业务说明 */
}

// ProcessIdentity 是所属订单提供给充电过程读取的订单、设备与端口编号，不包含网关或权限配置。
type ProcessIdentity struct {
	ID       uint64 // 中央订单主键
	OrderNo  string // 业务订单号
	DeviceID string // 设备全局编号
	PortNo   uint8  // 物理端口，从 1 开始
}

// findProcessIdentity 读取未删除订单的 ID、单号、设备编号与端口号。
// 调用前由 QueryService 校验权限，不信任客户端提供的设备或单号。
func findProcessIdentity(ctx context.Context, db *gorm.DB, id uint64) (ProcessIdentity, error) {
	var row ProcessIdentity
	err := db.WithContext(ctx).Table("charge_order").Select("id,order_no,device_id,port_no").Where("id=? AND deleted_at IS NULL", id).Take(&row).Error
	return row, err
}

// OrderExists 确认订单存在且未软删除。时间线接口先确认订单存在。
// 免得对不存在的订单返回一份空时间线。
// ctx 取消 db 查询，id 为订单主键；返回 nil 或缺行、数据库错误，不修改状态，授权由调用方负责。
func OrderExists(ctx context.Context, db *gorm.DB, id uint64) error {
	var row struct {
		ID uint64 // 未删除订单主键
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
// ctx 控制 db 查询，id 为已授权订单；返回记录或缺行、数据库错误，不查询实时遥测。
func FindOrderView(ctx context.Context, db *gorm.DB, id uint64) (OrderView, error) {
	var row OrderView
	err := orderBaseQuery(ctx, db).Select(orderColumns).Where("c.id = ?", id).Take(&row).Error
	return row, err
}
