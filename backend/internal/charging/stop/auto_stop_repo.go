package stop

import (
	"context"
	"time"
	orderdomain "wattharbor/internal/domain/order"

	"gorm.io/gorm"
)

// 停机扫描的订单及截止点只读查询结果；card 购买记录由 card 的 repo 提供。

// AutoStopOrder 是停机扫描读到的一笔充电中订单，含下单时冻结的计价快照。
type AutoStopOrder struct {
	ID              uint64     `gorm:"column:id"`               // 充电订单主键，分页游标
	OrderNo         string     `gorm:"column:order_no"`         // 网关校验停机身份的订单号
	UserID          uint64     `gorm:"column:user_id"`          // 订单所属用户主键
	DeviceID        string     `gorm:"column:device_id"`        // 网关设备业务标识
	PortNo          uint8      `gorm:"column:port_no"`          // 物理端口号，从 1 开始
	StartedAt       time.Time  `gorm:"column:started_at"`       // 实际开始充电的 UTC 时刻
	PricingSnapshot []byte     `gorm:"column:pricing_snapshot"` // 计价规则及套餐 JSON；已冻结恢复不依赖其存在
	CutoffAt        *time.Time `gorm:"column:cutoff_at"`        // 首次冻结的 UTC 截止时刻；nil 表示尚未决定停机
}

// ListChargingOrdersAfter 返回主键大于 afterID 的充电中订单，按主键递增。
// ctx 控制取消，db 为连接或事务，afterID=0 从首笔开始；无匹配返回空集，数据库失败返回错误。
// 递增游标让长时间运行的扫描不会因为新订单插入而漏读或重复读。
func ListChargingOrdersAfter(ctx context.Context, db *gorm.DB, afterID uint64) ([]AutoStopOrder, error) {
	rows := []AutoStopOrder{}
	err := db.WithContext(ctx).Table("charge_order c").
		Select("c.id,c.order_no,c.user_id,c.device_id,c.port_no,c.started_at,p.pricing_snapshot,b.cutoff_at").
		Joins("LEFT JOIN charge_order_pricing p ON p.charge_order_id=c.id").
		Joins("LEFT JOIN charge_billing_cutoff b ON b.charge_order_id=c.id").
		Where("c.status=? AND c.started_at IS NOT NULL AND c.deleted_at IS NULL AND c.id>?", orderdomain.StatusCharging, afterID).
		Order("c.id").Limit(autoStopBatchSize).Find(&rows).Error
	return rows, err
}

// autoStopBatchSize 限制每页订单数；心跳查询再按网关批量上限分组。
const autoStopBatchSize = 100
