package payment

import (
	"context"

	"gorm.io/gorm"
)

// 待起充订单的扫描。这张表在 central_db：worker 在进程拆分后不该再直接读
// central 的业务表，取数集中在这里，worker 只负责决定往哪几单下发。
//
// 三处条件缺一不可：订单已支付未启动、支付意图已付、支付单实付且金额匹配。
// 少任何一条都可能对一笔没真正付清的单下发启动指令。

// PaidChargeOrder 是待下发起充指令的一笔订单。
type PaidChargeOrder struct {
	ID      uint64 `gorm:"column:id"`
	OrderNo string `gorm:"column:order_no"`
}

// paidStartBatchSize 一次扫多少笔。游标按订单主键递增，重复调用会从上次位置继续。
const paidStartBatchSize = 100

// ListPaidOrdersAfter 返回主键大于 afterID 的待起充订单，按主键递增。
func ListPaidOrdersAfter(ctx context.Context, db *gorm.DB, afterID uint64) ([]PaidChargeOrder, error) {
	rows := []PaidChargeOrder{}
	err := db.WithContext(ctx).Table("charge_order AS c").
		Select("c.id, c.order_no").
		Joins("JOIN charge_payment_intent AS i ON i.charge_order_id = c.id AND i.user_id = c.user_id AND i.status = 'paid'").
		Joins("JOIN payment_order AS p ON p.id = i.payment_order_id AND p.biz_type = 'charge' "+
			"AND p.biz_id = c.id AND p.user_id = c.user_id AND p.status = 'paid' "+
			"AND p.total_cents > 0 AND p.paid_cents >= p.total_cents").
		Where("c.status = 'paid' AND c.deleted_at IS NULL AND c.id > ?", afterID).
		Order("c.id").Limit(paidStartBatchSize).Find(&rows).Error
	return rows, err
}
