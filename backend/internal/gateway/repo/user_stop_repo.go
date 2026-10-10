package repo

import (
	"chargepilot/internal/domain/devices"
	"chargepilot/internal/domain/money"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"

	"chargepilot/internal/protocol"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// ReserveUserStop 在 READ COMMITTED 事务内先锁启动命令、再锁端口，创建稳定 STOP 命令；重复请求复用已保存的命令。
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

// MarkUserStopSent 将 pending 或 sent 用户停机命令标记 sent 并更新发送时间；返回 SQL 错误，不确认设备断电。
func (s MySQLSink) MarkUserStopSent(ctx context.Context, commandID string) error {
	// SQL 内 status IN ('pending','sent')／status = 'sent' 分别对应
	// devices.StopCommandPending、StopCommandSent／StopCommandSent（charge_stop_command.status）。
	return s.DB.WithContext(ctx).Model(&chargeStopCommandRow{}).Where("command_id = ? AND status IN ('pending','sent')", commandID).
		Updates(map[string]any{"status": devices.StopCommandSent, "sent_at": gorm.Expr("CURRENT_TIMESTAMP(3)")}).Error
}

// PendingUserStops 按创建时间读取最多 50 条待发或已发送超过 10 秒的停机命令，复用 STOP 会话。
func (s MySQLSink) PendingUserStops(ctx context.Context) ([]StopReservation, error) {
	var rows []chargeStopCommandRow
	// SQL 内 status = 'pending'／status = 'sent' 对应 devices.StopCommandPending／StopCommandSent。
	err := s.DB.WithContext(ctx).Where("status = 'pending' OR (status = 'sent' AND sent_at < DATE_SUB(NOW(3), INTERVAL 10 SECOND))").
		Order("created_at").Limit(50).Find(&rows).Error
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
