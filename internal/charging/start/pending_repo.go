package start

import (
	"context"

	"gorm.io/gorm"
)

// 待起充订单由 central 的内部端点提供；worker 不持有查询这些业务表的职责。
//
// 三处条件缺一不可：订单已支付未启动、支付意图已付、支付单实付且金额匹配。
// 少任何一条都可能对一笔没真正付清的单下发启动指令。

// PaidChargeOrder 是待下发起充指令的一笔订单。
type PaidChargeOrder struct {
	ID      uint64 `gorm:"column:id" json:"id"`             // 原订单主键，作为递增游标
	OrderNo string `gorm:"column:order_no" json:"order_no"` // 原订单号，用于具名启动请求
}

// paidStartBatchSize 一次扫多少笔。游标按订单主键递增，重复调用会从上次位置继续。
const paidStartBatchSize = 100

// PendingOrders 查询已付清但尚未启动的订单，供 worker 有界扫描。
type PendingOrders struct {
	DB *gorm.DB /* central 订单只读连接，生命周期由进程管理 */
}

// PaidStarts 返回主键大于 afterID 的待起充订单，按主键递增。
// ctx 取消读取，afterID=0 从头扫描；空集合、nil 表示当前无下一页，错误表示未完成扫描。
func (s PendingOrders) PaidStarts(ctx context.Context, afterID uint64) ([]PaidChargeOrder, error) {
	rows := []PaidChargeOrder{}
	err := s.DB.WithContext(ctx).Table("charge_order AS c").
		Select("c.id, c.order_no").
		Joins("JOIN charge_payment_intent AS i ON i.charge_order_id = c.id AND i.user_id = c.user_id AND i.status = 'paid'").
		Joins("JOIN payment_order AS p ON p.id = i.payment_order_id AND p.biz_type = 'charge' "+
			"AND p.biz_id = c.id AND p.user_id = c.user_id AND p.status = 'paid' "+
			"AND p.total_cents > 0 AND p.paid_cents >= p.total_cents").
		Where("c.status = 'paid' AND c.deleted_at IS NULL AND c.id > ?", afterID).
		Order("c.id").Limit(paidStartBatchSize).Find(&rows).Error
	return rows, err
}
