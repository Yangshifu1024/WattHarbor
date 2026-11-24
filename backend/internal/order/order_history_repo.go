package order

import (
	"context"
	"gorm.io/gorm"
	"time"
	orderdomain "wattharbor/internal/domain/order"
	"wattharbor/internal/persist"
)

// UserOrderHistoryRow 是用户充电历史里的一行。费用三项为 nil 表示尚未计费完成。
// 未确认费用保持 nil，已确认零费用保持非空零值。
type UserOrderHistoryRow struct {
	OrderID        uint64                  `json:"order_id" gorm:"column:id"`                     // 充电订单主键
	OrderNo        string                  `json:"order_no" gorm:"column:order_no"`               // 业务充电单号
	DeviceID       string                  `json:"device_id" gorm:"column:device_id"`             // 设备全局编号
	PortNo         uint8                   `json:"port_no" gorm:"column:port_no"`                 // 设备端口号，从 1 开始
	Status         orderdomain.OrderStatus `json:"status" gorm:"column:status"`                   // 订单状态；申请退款不等于退款成功
	StartedAt      *time.Time              `json:"started_at" gorm:"column:started_at"`           // 实际启动 UTC 时刻，未开始为空
	EndedAt        *time.Time              `json:"ended_at" gorm:"column:ended_at"`               // 结束 UTC 时刻，未结束为空
	ChargedKWh     *string                 `json:"charged_kwh" gorm:"column:charged_kwh"`         // 累计电量，kWh，保持DECIMAL字符串；缺失为空
	ChargedSeconds *uint32                 `json:"charged_seconds" gorm:"column:charged_seconds"` // 充电累计时长，秒，缺失为空
	ElectricCents  *int64                  `json:"electric_cents" gorm:"column:electric_cents"`   // 已确认电费，分，未计费为空
	ServiceCents   *int64                  `json:"service_cents" gorm:"column:service_cents"`     // 已确认服务费，分，未计费为空
	TotalCents     *int64                  `json:"total_cents" gorm:"column:total_cents"`         // 已确认总费，分，未计费为空
	DiscountCents  int64                   `json:"discount_cents" gorm:"column:discount_cents"`   // 优惠抵扣，分
}

// ListUserOrderHistory 用 db 按 userID、status 和已校验 page/pageSize 返回订单页。
// 稳定按主键倒序，始终隔离用户；费用由对应模块的接口一次批量回填。
func ListUserOrderHistory(ctx context.Context, db *gorm.DB, userID uint64, status string, page, pageSize int) (persist.Page[UserOrderHistoryRow], error) {
	out := persist.Page[UserOrderHistoryRow]{Items: []UserOrderHistoryRow{}, Page: page, PageSize: pageSize}
	query := db.WithContext(ctx).Table("charge_order").Where("user_id = ? AND deleted_at IS NULL", userID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	// 费用在 charge_fee_receipt.result_json 里，不在订单表上，所以这三列此刻查不出来。
	// 由调用方按批回填。显式列出列名，避免 GORM 按字段名推导取错列。
	err := query.Select("id, order_no, device_id, port_no, status, started_at, ended_at, " +
		"charged_kwh, charged_seconds, discount_cents").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out.Items).Error
	return out, err
}
