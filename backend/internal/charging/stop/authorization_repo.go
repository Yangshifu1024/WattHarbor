package stop

import (
	"context"
	"database/sql"
	orderdomain "wattharbor/internal/domain/order"

	"gorm.io/gorm"
)

// ActiveStopAuthorization 是一笔可授权停机的订单。
type ActiveStopAuthorization struct {
	ChargeOrderID  uint64        // 正在充电的订单主键
	OrderNo        string        // 业务单号，停止请求不得重新生成编号
	UserID         uint64        // 已核对的订单所属用户
	DeviceID       string        // 成功启动回执所归属设备
	PortNo         uint8         // 目标设备端口序号，从 1 开始
	PortID         sql.NullInt64 // 端口编号；NULL 表示起充回执没带回端口，不能下发停机
	StartCommandID string        // 起充指令 ID，回报停机结果时带回
}

// FindActiveStopAuthorization 查询属于用户、正在充电且已有成功起充回执的订单。
// 不满足条件时返回 gorm.ErrRecordNotFound，其他读取错误也不能作为停机授权。
func FindActiveStopAuthorization(ctx context.Context, db *gorm.DB, orderNo string, userID uint64) (ActiveStopAuthorization, error) {
	var row ActiveStopAuthorization
	err := db.WithContext(ctx).Table("charge_order AS c").
		Select("c.id AS charge_order_id, c.order_no, c.user_id, c.device_id, c.port_no, s.port_id, s.command_id AS start_command_id").
		Joins("JOIN charge_start_receipt AS s ON s.charge_order_id = c.id AND s.success = TRUE").
		Where("c.order_no = ? AND c.user_id = ? AND c.status = ? AND c.deleted_at IS NULL AND s.port_id IS NOT NULL", orderNo, userID, orderdomain.StatusCharging).
		Take(&row).Error
	return row, err
}
