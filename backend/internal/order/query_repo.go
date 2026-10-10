package order

import (
	orderdomain "chargepilot/internal/domain/order"
	"context"
	"gorm.io/gorm"
)

// QueryRepo 读取用户负责的订单，表归属与请求隔离集中于对应仓储。
type QueryRepo struct {
	DB *gorm.DB /* 所属订单连接或调用方事务 */
}

// Owned 按 userID 和 no 业务单号读取未删除订单，返回信息，缺失时返回错误。
func (s QueryRepo) Owned(ctx context.Context, userID uint64, no string) (ChargeOrderRecord, error) {
	var row ChargeOrderRecord
	err := s.DB.WithContext(ctx).Where("order_no=? AND user_id=? AND deleted_at IS NULL", no, userID).Take(&row).Error
	return row, err
}

// Ongoing 返回 userID 最新 paid/charging 未删除订单，缺失时返回错误，不发起充电或修改端口。
func (s QueryRepo) Ongoing(ctx context.Context, userID uint64) (ChargeOrderRecord, error) {
	var row ChargeOrderRecord
	err := s.DB.WithContext(ctx).Where("user_id=? AND deleted_at IS NULL AND status IN ?", userID, paidOrChargingOrders()).Order("id DESC").Take(&row).Error
	return row, err
}

// paidOrChargingOrders 返回用户存在进行中订单的状态集合：已支付未起充与充电中。
// 集合与 billing 侧判定进行中订单的口径必须一致，否则两个入口会看到不同订单。
func paidOrChargingOrders() []orderdomain.OrderStatus {
	return []orderdomain.OrderStatus{orderdomain.StatusPaid, orderdomain.StatusCharging}
}
