package stop

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 停机扫描的订单与已购时长查询；设备证据判定位于 auto_stop.go 和 meter.go。

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

// autoStopBatchSize 限制每页充电中订单数；网关取证再按共享契约的批量上限分批。
const autoStopBatchSize = 100

// PurchasedMinutesForOrder 返回这笔订单用卡购买的时长，单位分钟。
// 无刷卡记录时返回 0，调用方继续使用订单冻结的套餐时长。
func PurchasedMinutesForOrder(ctx context.Context, db *gorm.DB, orderID uint64) (uint16, error) {
	var card struct {
		PurchasedMinutes uint16 `gorm:"column:purchased_minutes"`
	}
	err := db.WithContext(ctx).Table("card_charge").
		Where("charge_order_id=?", orderID).Find(&card).Error
	return card.PurchasedMinutes, err
}
