package stop

import (
	"context"
	"database/sql"

	"gorm.io/gorm"
)

// ActiveStopAuthorization 是一笔可授权停机的订单。
type ActiveStopAuthorization struct {
	ChargeOrderID  uint64
	OrderNo        string
	UserID         uint64
	DeviceID       string
	PortNo         uint8
	PortID         sql.NullInt64 // 端口身份；NULL 表示起充回执没带回端口，不能下发停机
	StartCommandID string        // 起充指令 ID，回报停机结果时带回
}

// FindActiveStopAuthorization 取一笔正在充电、属于该用户、且已有成功起充回执的订单。
func FindActiveStopAuthorization(ctx context.Context, db *gorm.DB, orderNo string, userID uint64) (ActiveStopAuthorization, error) {
	var row ActiveStopAuthorization
	err := db.WithContext(ctx).Table("charge_order AS c").
		Select("c.id AS charge_order_id, c.order_no, c.user_id, c.device_id, c.port_no, s.port_id, s.command_id AS start_command_id").
		Joins("JOIN charge_start_receipt AS s ON s.charge_order_id = c.id AND s.success = TRUE").
		Where("c.order_no = ? AND c.user_id = ? AND c.status = 'charging' AND c.deleted_at IS NULL AND s.port_id IS NOT NULL", orderNo, userID).
		Take(&row).Error
	return row, err
}
