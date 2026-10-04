package order

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"gorm.io/gorm"
	"time"
)

// UserOrderHistoryRow 是用户充电历史里的一行。费用三项为 nil 表示尚未计费完成，
// 不能用 0 顶替——"还没算出来"和"算出��是 0"对用户是两件事。
type UserOrderHistoryRow struct {
	OrderID        uint64     `json:"order_id" gorm:"column:id"`
	OrderNo        string     `json:"order_no" gorm:"column:order_no"`
	DeviceID       string     `json:"device_id" gorm:"column:device_id"`
	PortNo         uint8      `json:"port_no" gorm:"column:port_no"`
	Status         string     `json:"status" gorm:"column:status"`
	StartedAt      *time.Time `json:"started_at" gorm:"column:started_at"`
	EndedAt        *time.Time `json:"ended_at" gorm:"column:ended_at"`
	ChargedKWh     *string    `json:"charged_kwh" gorm:"column:charged_kwh"`
	ChargedSeconds *uint32    `json:"charged_seconds" gorm:"column:charged_seconds"`
	ElectricCents  *int64     `json:"electric_cents" gorm:"column:electric_cents"`
	ServiceCents   *int64     `json:"service_cents" gorm:"column:service_cents"`
	TotalCents     *int64     `json:"total_cents" gorm:"column:total_cents"`
	DiscountCents  int64      `json:"discount_cents" gorm:"column:discount_cents"`
}

// ListUserOrderHistory 分页返回用户的充电历史，可按订单状态过滤。
func ListUserOrderHistory(ctx context.Context, db *gorm.DB, userID uint64, status string, page, pageSize int) (persist.Page[UserOrderHistoryRow], error) {
	out := persist.Page[UserOrderHistoryRow]{Items: []UserOrderHistoryRow{}, Page: page, PageSize: pageSize}
	query := db.WithContext(ctx).Table("charge_order").Where("user_id = ? AND deleted_at IS NULL", userID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	// 费用在 charge_fee_receipt.result_json 里，不在订单表上，所以这三列此刻查不出来，
	// 由调用方按批回填。显式列出列名，避免 GORM 按字段名推导取错列。
	err := query.Select("id, order_no, device_id, port_no, status, started_at, ended_at, " +
		"charged_kwh, charged_seconds, discount_cents").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out.Items).Error
	return out, err
}
