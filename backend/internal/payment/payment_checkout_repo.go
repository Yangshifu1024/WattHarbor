// 支付申请检查端口占用并分配支付单号；订单状态只用于查询。
package payment

import (
	"strconv"
	orderdomain "wattharbor/internal/domain/order"

	"gorm.io/gorm"
	"wattharbor/internal/platform/snowflake"
)

// CheckoutPortAvailable 确认端口上没有未过期支付意图或进行中的订单。
// 同端口并发支付意图由 charge_payment_intent.uk_active_port 唯一键仲裁。
// 本查询不读取 card_charge，刷卡占用及设备最终启动另由对应用例核对。
// tx 为申请事务，port 为端口编码；已有占用返回 ErrPaymentIntentConflict，查询失败返回数据库错误。
// 本查询不加锁；并发创建仍由端口唯一键拒绝重复预占。
func CheckoutPortAvailable(tx *gorm.DB, port string) error {
	var count int64
	err := tx.Table("charge_payment_intent i").Joins("LEFT JOIN charge_order c ON c.id=i.charge_order_id").
		Where("i.port_code=? AND ((i.status='initiated' AND i.expires_at>UTC_TIMESTAMP(3)) OR (i.status='paid' AND c.status IN ?))", port, paidOrChargingOrderStatuses()).Count(&count).Error
	if err != nil {
		return err
	}
	if count != 0 {
		return ErrPaymentIntentConflict
	}
	return nil
}

// NewOrderNumber 分配与支付来源和用户注册无关的独立支付单号（雪花序列表）。
// tx 是调用方业务事务，返回 P 前缀编号及序列分配错误；编号分配随事务提交，不请求外部服务。
func NewOrderNumber(tx *gorm.DB) (string, error) {
	id, err := snowflake.Next(tx)
	if err != nil {
		return "", err
	}
	return "P" + strconv.FormatUint(id, 10), nil
}

// paidOrChargingOrderStatuses 返回占用端口的订单状态集合：已支付未起充与充电中。
// 该集合只用于判断端口是否已有进行中的订单，与订单侧 Ongoing 的口径必须一致。
func paidOrChargingOrderStatuses() []orderdomain.OrderStatus {
	return []orderdomain.OrderStatus{orderdomain.StatusPaid, orderdomain.StatusCharging}
}
