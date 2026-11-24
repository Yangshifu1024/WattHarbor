package repo

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"wattharbor/internal/domain/devices"
	"wattharbor/internal/domain/money"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"wattharbor/internal/protocol"
)

// ActiveOrder 保存中央停机授权转换得到的充电订单、用户和已确认启动及端口编号。
type ActiveOrder struct {
	// ChargeOrderID 中央充电订单编号。
	ChargeOrderID uint64 `json:"charge_order_id"`
	// OrderNo 业务订单号。
	OrderNo string `json:"order_no"`
	// UserID 订单用户编号。
	UserID uint64 `json:"user_id"`
	// DeviceID 设备业务编号。
	DeviceID string `json:"device_id"`
	// PortNo 协议端口序号。
	PortNo uint8 `json:"port_no"`
	// PortID 已授权占用端口的 device_port 自增主键。
	PortID uint64 `json:"port_id"`
	// StartCommandID 停机对应的 START 命令 UUID。
	StartCommandID string `json:"start_command_id"`
}

// StopReservation 保存已受理停机的命令编号、状态和协议报文；不表示设备已经断电。
type StopReservation struct {
	// CommandID 持久命令 UUID。
	CommandID string `json:"command_id"`
	// OrderNo 业务订单号。
	OrderNo string `json:"order_no"`
	// DeviceID 设备业务编号。
	DeviceID string `json:"device_id"`
	// PortNo 协议端口序号。
	PortNo uint8 `json:"port_no"`
	// Status 命令持久化状态，对应 charge_stop_command.status（该列没有 stopping）。
	Status devices.StopCommandStatus `json:"status"`
	// Wire 待下发的协议命令，不表示设备已确认。
	Wire protocol.Command `json:"-"`
}

// ExistingUserStop 读取用户停机命令并核对所有者；返回受理结果或缺失、冲突、SQL 错误，不写库。
func (s MySQLSink) ExistingUserStop(ctx context.Context, orderNo string, userID uint64) (StopReservation, error) {
	var row chargeStopCommandRow
	if err := s.DB.WithContext(ctx).Where("order_no = ?", orderNo).Take(&row).Error; err != nil {
		return StopReservation{}, err
	}
	if row.UserID != userID {
		return StopReservation{}, ErrOrderConflict
	}
	return stopReservationFromRow(row)
}

// ReserveUserStop 在 READ COMMITTED 事务内锁启动命令、重读已有 STOP、再锁端口；重复请求复用已保存的命令。
func (s MySQLSink) ReserveUserStop(ctx context.Context, active ActiveOrder) (StopReservation, error) {
	if active.ChargeOrderID == 0 || active.OrderNo == "" || active.UserID == 0 || active.DeviceID == "" || active.PortNo == 0 || active.PortID == 0 || active.StartCommandID == "" {
		return StopReservation{}, ErrOrderConflict
	}
	if existing, err := s.ExistingUserStop(ctx, active.OrderNo, active.UserID); err == nil {
		return existing, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return StopReservation{}, err
	}
	var reservation StopReservation
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var start chargeCommandRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id = ? AND order_no = ? AND user_id = ?", active.ChargeOrderID, active.OrderNo, active.UserID).Take(&start).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrOrderConflict
		}
		if err != nil {
			return err
		}
		// 原 START 锁将创建串行化；等待后须当前读，避免两个首次请求撞上订单唯一键。
		var existing chargeStopCommandRow
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no=?", active.OrderNo).Take(&existing).Error
		if err == nil {
			if existing.UserID != active.UserID {
				return ErrOrderConflict
			}
			reservation, err = stopReservationFromRow(existing)
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if start.Status != devices.CommandAcked || start.CommandID != active.StartCommandID || start.DeviceID != active.DeviceID || start.PortNo != active.PortNo || !start.PortID.Valid || uint64(start.PortID.Int64) != active.PortID {
			return ErrOrderConflict
		}
		var port devicePortRow
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND device_id = ? AND port_no = ? AND current_order_id = ? AND status = ?", active.PortID, active.DeviceID, active.PortNo, active.OrderNo, money.PortCharging).Take(&port).Error
		if err != nil {
			return ErrPortUnavailable
		}
		var session [6]byte
		if _, err := rand.Read(session[:]); err != nil {
			return err
		}
		commandID := uuid.NewString()
		command := chargeStopCommandRow{CommandID: commandID, StartCommandID: active.StartCommandID, ChargeOrderID: active.ChargeOrderID,
			OrderNo: active.OrderNo, UserID: active.UserID, DeviceID: active.DeviceID, PortNo: active.PortNo,
			PortID: sql.NullInt64{Int64: int64(active.PortID), Valid: true}, Status: devices.StopCommandPending,
			SessionID: sql.NullString{String: hex.EncodeToString(session[:]), Valid: true}}
		if err := tx.Create(&command).Error; err != nil {
			return err
		}
		reservation = StopReservation{CommandID: commandID, OrderNo: active.OrderNo, DeviceID: active.DeviceID, PortNo: active.PortNo,
			Status: devices.StopCommandPending, Wire: protocol.Command{Kind: protocol.CommandStop, SessionID: session, Port: active.PortNo}}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return StopReservation{}, err
	}
	return reservation, nil
}

// MarkUserStopSent 在同设备许可内核对原 START 和当前占用，再保存发送尝试；返回是否仍可发送。
// 零影响表示命令已终结或占用已释放/换单，不发旧 STOP；SQL 失败返回错误，不确认断电。
func (s MySQLSink) MarkUserStopSent(ctx context.Context, commandID string) (bool, error) {
	res := s.DB.WithContext(ctx).Exec(`UPDATE charge_stop_command s
JOIN charge_command c ON c.command_id=s.start_command_id AND c.charge_order_id=s.charge_order_id AND c.order_no=s.order_no
JOIN device_port p ON p.id=s.port_id AND p.id=c.port_id AND p.device_id=s.device_id AND p.port_no=s.port_no
SET s.status=?, s.sent_at=CURRENT_TIMESTAMP(3)
WHERE s.command_id=? AND s.status IN ? AND c.status=? AND c.device_id=s.device_id AND c.port_no=s.port_no
AND p.current_order_id=s.order_no AND p.status=?`, devices.StopCommandSent, commandID,
		[]devices.StopCommandStatus{devices.StopCommandPending, devices.StopCommandSent}, devices.CommandAcked, money.PortCharging)
	return res.RowsAffected == 1, res.Error
}

// PendingUserStops 先按在线设备和原订单占用过滤，再按最近发送尝试读取最多 50 条到期 STOP。
// online 是连接登记快照，空集合不查询；执行时须重新核对归属和连接，旧记录保留但不再派发。
func (s MySQLSink) PendingUserStops(ctx context.Context, online []string) ([]StopReservation, error) {
	if len(online) == 0 {
		return []StopReservation{}, nil
	}
	var rows []chargeStopCommandRow
	// SQL 内 status = 'pending'／status = 'sent' 对应 devices.StopCommandPending／StopCommandSent。
	err := s.DB.WithContext(ctx).Table("charge_stop_command s").Select("s.*").
		Joins("JOIN charge_command c ON c.command_id=s.start_command_id AND c.charge_order_id=s.charge_order_id AND c.order_no=s.order_no AND c.device_id=s.device_id AND c.port_no=s.port_no").
		Joins("JOIN device_port p ON p.id=s.port_id AND p.id=c.port_id AND p.device_id=s.device_id AND p.port_no=s.port_no AND p.current_order_id=s.order_no").
		Where("s.device_id IN ? AND c.status=? AND p.status=?", online, devices.CommandAcked, money.PortCharging).
		Where("s.status = ? OR (s.status = ? AND s.sent_at < DATE_SUB(NOW(3), INTERVAL 10 SECOND))", devices.StopCommandPending, devices.StopCommandSent).
		Order("COALESCE(s.sent_at,s.created_at),s.command_id").Limit(50).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]StopReservation, 0, len(rows))
	for _, row := range rows {
		wire, err := stopJob(row.CommandID, row.OrderNo, row.DeviceID, row.PortNo, row.SessionID)
		if err != nil {
			return nil, err
		}
		result = append(result, StopReservation{CommandID: row.CommandID, OrderNo: row.OrderNo, DeviceID: row.DeviceID, PortNo: row.PortNo, Status: devices.StopCommandPending, Wire: wire.Wire})
	}
	return result, nil
}

// stopReservationFromRow 接收停机 row，解码并还原 STOP 与已保存状态；返回受理结果或会话错误。
func stopReservationFromRow(row chargeStopCommandRow) (StopReservation, error) {
	job, err := stopJob(row.CommandID, row.OrderNo, row.DeviceID, row.PortNo, row.SessionID)
	if err != nil {
		return StopReservation{}, err
	}
	return StopReservation{CommandID: row.CommandID, OrderNo: row.OrderNo, DeviceID: row.DeviceID,
		PortNo: row.PortNo, Status: row.Status, Wire: job.Wire}, nil
}
