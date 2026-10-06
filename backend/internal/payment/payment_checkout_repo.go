// 支付意图端口占用和支付编号归 payment；订单生命周期仅作只读联表事实。
package payment

import (
	"strconv"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/snowflake"
	"gorm.io/gorm"
)

// CheckoutPortAvailable 确认端口上没有未过期支付意图或进行中的订单。
// 同端口并发支付意图由 charge_payment_intent.uk_active_port 唯一键仲裁；
// 本查询不读取 card_charge，刷卡占用及设备最终启动另由对应用例核对。
// tx 为调用方申请事务，port 为原业务端口编码；返回 ErrPaymentIntentConflict 或读取错误。
// 普通计数用于准入，最终插入唯一键仲裁并发，不在此创建或提交预占。
func CheckoutPortAvailable(tx *gorm.DB, port string) error {
	var count int64
	err := tx.Table("charge_payment_intent i").Joins("LEFT JOIN charge_order c ON c.id=i.charge_order_id").
		Where("i.port_code=? AND ((i.status='initiated' AND i.expires_at>UTC_TIMESTAMP(3)) OR (i.status='paid' AND c.status IN ('paid','charging')))", port).Count(&count).Error
	if err != nil {
		return err
	}
	if count != 0 {
		return ErrPaymentIntentConflict
	}
	return nil
}

// NewOrderNumber 分配与支付来源和用户注册无关的独立支付单号（雪花序列表）。
// tx 是调用方原业务事务，返回 P 前缀编号及序列分配错误；编号分配随事务提交，不请求外部服务。
func NewOrderNumber(tx *gorm.DB) (string, error) {
	id, err := snowflake.Next(tx)
	if err != nil {
		return "", err
	}
	return "P" + strconv.FormatUint(id, 10), nil
}
