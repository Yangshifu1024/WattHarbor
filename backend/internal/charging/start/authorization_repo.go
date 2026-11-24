package start

import (
	"context"
	"wattharbor/internal/domain/money"
	orderdomain "wattharbor/internal/domain/order"

	"gorm.io/gorm"
)

// PaidStartAuthorization 是一笔可授权起充的订单。
type PaidStartAuthorization struct {
	ChargeOrderID  uint64 // 已付清订单主键
	OrderNo        string // 业务订单号，用于启动幂等编号
	UserID         uint64 // 订单与支付共同所属用户
	DeviceID       string // 目标设备编号
	PortNo         uint8  // 设备端口序号，从 1 开始
	PaymentOrderID uint64 // 已确认到账的支付编号
	ChargeMode     uint8  // 订单冻结的设备执行模式，0/4 为时长、1 为电量
	ChargeQuantity uint16 // 模式对应执行量；时长单位分钟，电量按协议缩放值
}

// FindPaidStartAuthorization 读取已付清订单，核对支付归属、设备模式和执行额度范围。
// 取不到返回 gorm.ErrRecordNotFound，调用方据此拒绝下发。
// ctx 取消读取；db 为 central 连接，orderNo 为业务单号；数据库错误不构成启动授权。
func FindPaidStartAuthorization(ctx context.Context, db *gorm.DB, orderNo string) (PaidStartAuthorization, error) {
	var row PaidStartAuthorization
	err := db.WithContext(ctx).Table("charge_order AS c").
		Select(`c.id AS charge_order_id, c.order_no, c.user_id, c.device_id, c.port_no,
			p.id AS payment_order_id, c.charge_mode, c.charge_quantity`).
		Joins(`JOIN payment_order AS p ON p.id = c.payment_order_id
			AND p.biz_type = ? AND p.biz_id = c.id AND p.user_id = c.user_id AND p.status = ?`, money.PaymentBizCharge, money.PayStatusPaid).
		Where(`c.order_no = ? AND c.status = ? AND c.deleted_at IS NULL
			AND p.paid_cents >= p.total_cents
			AND p.total_cents > 0 AND p.deleted_at IS NULL
			AND ((c.charge_mode IN (0,4) AND c.charge_quantity BETWEEN 1 AND 4320)
			OR (c.charge_mode = 1 AND c.charge_quantity BETWEEN 1000 AND 65000))`, orderNo, orderdomain.StatusPaid).
		Take(&row).Error
	return row, err
}
