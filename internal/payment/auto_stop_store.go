package payment

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 停机扫描需要的持久化读取与写入。停机判定本身在 application/auto_stop.go：
// 它要结合设备心跳证据做决策，那是业务规则。这里只提供"取数据"和"冻结计费截止"。
//
// 计费截止点的冻结是首写生效：已有记录说明这一点已冻结过，调用方据此判为
// 冲突并让外层重试，而不是静默更新——静默更新会让两次停机看起来像同一次，
// 掩盖重复触发。

// AutoStopOrder 是停机扫描读到的一笔充电中订单，含下单时冻结的计价快照。
type AutoStopOrder struct {
	ID              uint64    `gorm:"column:id"`
	OrderNo         string    `gorm:"column:order_no"`
	UserID          uint64    `gorm:"column:user_id"`
	DeviceID        string    `gorm:"column:device_id"`
	PortNo          uint8     `gorm:"column:port_no"`
	StartedAt       time.Time `gorm:"column:started_at"`
	PricingSnapshot []byte    `gorm:"column:pricing_snapshot"`
}

// ListChargingOrdersAfter 返回主键大于 afterID 的充电中订单，按主键递增。
// 递增游标让长时间运行的扫描不会因为新订单插入而漏读或重复读。
func ListChargingOrdersAfter(ctx context.Context, db *gorm.DB, afterID uint64) ([]AutoStopOrder, error) {
	rows := []AutoStopOrder{}
	err := db.WithContext(ctx).Table("charge_order c").
		Select("c.id,c.order_no,c.user_id,c.device_id,c.port_no,c.started_at,p.pricing_snapshot").
		Joins("JOIN charge_order_pricing p ON p.charge_order_id=c.id").
		Where("c.status='charging' AND c.started_at IS NOT NULL AND c.deleted_at IS NULL AND c.id>?", afterID).
		Order("c.id").Limit(autoStopBatchSize).Find(&rows).Error
	return rows, err
}

// autoStopBatchSize 是一页取多少笔充电中订单。与 gateway 批量取证端点的分块
// 大小一致，让一次扫描的往返次数可预期。
const autoStopBatchSize = 100

// PurchasedMinutesForOrder 返回这笔订单用卡购买的时长，单位分钟。
// 查不到或没有记录都返回 0，调用方按"不限时"处理。
func PurchasedMinutesForOrder(ctx context.Context, db *gorm.DB, orderID uint64) (uint16, error) {
	var card struct {
		PurchasedMinutes uint16 `gorm:"column:purchased_minutes"`
	}
	err := db.WithContext(ctx).Table("card_charge").
		Where("charge_order_id=?", orderID).Find(&card).Error
	return card.PurchasedMinutes, err
}

// BillingCutoffFrozen 数出该订单已有的计费截止记录数。大于 0 表示已经冻结过。
func BillingCutoffFrozen(ctx context.Context, db *gorm.DB, orderID uint64) (int64, error) {
	var frozen int64
	err := db.WithContext(ctx).Table("charge_billing_cutoff").
		Where("charge_order_id=?", orderID).Count(&frozen).Error
	return frozen, err
}

// FreezeBillingCutoff 为订单首写计费截止点。调用方需先用 BillingCutoffFrozen
// 确认尚未冻结——这里不做判断，因为"是否重复"是业务判断，不是存储细节。
// row 带上截止时刻、原因与计量快照，由调用方组装。
func FreezeBillingCutoff(tx *gorm.DB, orderID uint64, row map[string]any) error {
	return tx.Table("charge_billing_cutoff").Create(row).Error
}
