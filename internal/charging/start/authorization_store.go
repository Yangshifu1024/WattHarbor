package start

import (
	"context"

	"gorm.io/gorm"
)

// PaidStartAuthorization 是一笔可授权起充的订单。
type PaidStartAuthorization struct {
	ChargeOrderID  uint64
	OrderNo        string
	UserID         uint64
	DeviceID       string
	PortNo         uint8
	PaymentOrderID uint64
	ChargeMode     uint8
	ChargeQuantity uint16
}

// FindPaidStartAuthorization 取一笔已付清且满足计量区间约束的订单。
// 取不到返回 gorm.ErrRecordNotFound，调用方据此拒绝下发。
func FindPaidStartAuthorization(ctx context.Context, db *gorm.DB, orderNo string) (PaidStartAuthorization, error) {
	var row PaidStartAuthorization
	err := db.WithContext(ctx).Table("charge_order AS c").
		Select(`c.id AS charge_order_id, c.order_no, c.user_id, c.device_id, c.port_no,
			p.id AS payment_order_id, c.charge_mode, c.charge_quantity`).
		Joins(`JOIN payment_order AS p ON p.id = c.payment_order_id
			AND p.biz_type = 'charge' AND p.biz_id = c.id AND p.user_id = c.user_id`).
		Where(`c.order_no = ? AND c.status = 'paid' AND c.deleted_at IS NULL
			AND p.status = 'paid' AND p.paid_cents >= p.total_cents
			AND p.total_cents > 0 AND p.deleted_at IS NULL
			AND ((c.charge_mode IN (0,4) AND c.charge_quantity BETWEEN 1 AND 4320)
			OR (c.charge_mode = 1 AND c.charge_quantity BETWEEN 1000 AND 65000))`, orderNo).
		Take(&row).Error
	return row, err
}
