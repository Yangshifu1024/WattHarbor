package repo

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrPortUnavailable = errors.New("device port is unavailable")
var ErrOrderConflict = errors.New("charge command identity conflict")

// PaidOrder 保存中央已支付订单授权转换得到的原启动身份与协议限制，供网关受理校验，不是额外资金记录。
type PaidOrder struct {
	// ConsumerType 原协议消费类型，2 为扫码、3 为卡。
	ConsumerType uint8 `json:"consumer_type"`
	// CardNumber 原协议卡号。
	CardNumber uint32 `json:"card_number"`
	// CardBalanceUnits 原卡余额协议值，单位 0.1 元，由金额分值除以十得到。
	CardBalanceUnits uint16 `json:"card_balance_units"`
	// ChargeOrderID 中央充电订单身份。
	ChargeOrderID uint64 `json:"charge_order_id"`
	// PaymentOrderID 中央支付订单身份。
	PaymentOrderID uint64 `json:"payment_order_id"`
	// OrderNo 原业务订单号。
	OrderNo string `json:"order_no"`
	// UserID 原订单用户身份。
	UserID uint64 `json:"user_id"`
	// DeviceID 原设备业务编号。
	DeviceID string `json:"device_id"`
	// PortNo 原协议端口序号。
	PortNo uint8 `json:"port_no"`
	// ChargeMode 原协议充电模式。
	ChargeMode uint8 `json:"charge_mode"`
	// ChargeQuantity 原授权的协议充电量或时长单位值。
	ChargeQuantity uint16 `json:"charge_quantity"`
}

// StartReservation 保存启动已持久受理的原命令、状态与 START/补偿 STOP 报文，报文不参与公开 JSON。
type StartReservation struct {
	// CommandID 原持久命令 UUID。
	CommandID string `json:"command_id"`
	// Status 所属记录的原状态值。
	Status string `json:"status"`
	// OrderNo 原业务订单号。
	OrderNo string `json:"order_no"`
	// DeviceID 原设备业务编号。
	DeviceID string `json:"device_id"`
	// PortNo 原协议端口序号。
	PortNo uint8 `json:"port_no"`
	// Wire 待下发的原协议命令，不表示设备已确认。
	Wire protocol.Command `json:"-"`
	// StopWire 启动结果不确定时使用的原补偿 STOP 命令。
	StopWire protocol.Command `json:"-"`
}

// ReserveStart 接收 ctx 与已授权 paid 订单，在 TCP 下发前持久化端口占用与稳定命令会话；重放核对身份后复用原命令，返回受理事实或校验、冲突、SQL 错误。
func (s MySQLSink) ReserveStart(ctx context.Context, paid PaidOrder) (StartReservation, error) {
	if s.DB == nil || paid.ChargeOrderID == 0 || paid.PaymentOrderID == 0 || paid.OrderNo == "" || paid.UserID == 0 || paid.DeviceID == "" || paid.PortNo == 0 || paid.ChargeQuantity == 0 {
		return StartReservation{}, ErrOrderConflict
	}
	if paid.ConsumerType == 0 {
		paid.ConsumerType = 2
	}
	if paid.ConsumerType != 2 && paid.ConsumerType != 3 || paid.ConsumerType == 3 && paid.CardNumber == 0 || paid.ConsumerType == 2 && (paid.CardNumber != 0 || paid.CardBalanceUnits != 0) {
		return StartReservation{}, ErrOrderConflict
	}
	mode, quantity := paid.ChargeMode, paid.ChargeQuantity
	if mode != 0 && mode != 1 && mode != 4 {
		return StartReservation{}, ErrOrderConflict
	}
	if mode != 1 && quantity > 4320 {
		return StartReservation{}, ErrOrderConflict
	}
	orderBCD, err := numericOrderBCD(paid.ChargeOrderID)
	if err != nil {
		return StartReservation{}, err
	}
	var existing chargeCommandRow
	err = s.DB.WithContext(ctx).Where("order_no = ?", paid.OrderNo).Take(&existing).Error
	if err == nil {
		return reservationFromRow(existing, paid, orderBCD, mode, quantity)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return StartReservation{}, err
	}

	var reservation StartReservation
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var port devicePortRow
		err := tx.Table("device_port AS p").Select("p.id, p.port_code").
			Joins("JOIN device AS d ON d.device_id = p.device_id").
			Joins("JOIN vendor AS v ON v.id = d.vendor_id").
			Where(`p.device_id = ? AND p.port_no = ? AND p.deleted_at IS NULL AND p.status = 'idle' AND p.current_order_id IS NULL
				AND d.status = 'enabled' AND d.deleted_at IS NULL AND v.status = 'enabled' AND v.deleted_at IS NULL`, paid.DeviceID, paid.PortNo).
			Clauses(clause.Locking{Strength: "UPDATE"}).Take(&port).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPortUnavailable
		}
		if err != nil {
			return err
		}
		commandID, stopID := uuid.NewString(), uuid.NewString()
		var startSession, stopSession [6]byte
		if _, err := rand.Read(startSession[:]); err != nil {
			return err
		}
		if _, err := rand.Read(stopSession[:]); err != nil {
			return err
		}
		reserved := tx.Model(&devicePortRow{}).Where("id = ? AND status = 'idle' AND current_order_id IS NULL", port.ID).
			Update("current_order_id", paid.OrderNo)
		if err := requireOne(reserved); err != nil {
			return ErrPortUnavailable
		}
		command := chargeCommandRow{CommandID: commandID, StopCommandID: stopID,
			ChargeOrderID: paid.ChargeOrderID, PaymentOrderID: paid.PaymentOrderID, OrderNo: paid.OrderNo,
			UserID: paid.UserID, DeviceID: paid.DeviceID, PortNo: paid.PortNo, PortCode: port.PortCode,
			PortID: sql.NullInt64{Int64: int64(port.ID), Valid: true}, OwnsPort: true, Status: "pending",
			SessionID:     sql.NullString{String: hex.EncodeToString(startSession[:]), Valid: true},
			StopSessionID: sql.NullString{String: hex.EncodeToString(stopSession[:]), Valid: true}, ChargeMode: mode, Quantity: quantity, ConsumerType: paid.ConsumerType, CardNumber: paid.CardNumber, CardBalanceUnits: paid.CardBalanceUnits}
		if err := tx.Create(&command).Error; err != nil {
			return fmt.Errorf("insert charge command: %w", err)
		}
		reservation = StartReservation{CommandID: commandID, Status: "pending", OrderNo: paid.OrderNo,
			DeviceID: paid.DeviceID, PortNo: paid.PortNo,
			Wire:     protocol.Command{Kind: protocol.CommandStart, SessionID: startSession, Port: paid.PortNo, OrderBCD: orderBCD, Mode: mode, Quantity: quantity, ConsumerType: paid.ConsumerType, CardNumber: paid.CardNumber, CardBalanceUnits: paid.CardBalanceUnits},
			StopWire: protocol.Command{Kind: protocol.CommandStop, SessionID: stopSession, Port: paid.PortNo}}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if errors.Is(err, ErrPortUnavailable) {
		// 同一已支付订单的并发启动请求可能已完成端口占用。
		var raced chargeCommandRow
		lookupErr := s.DB.WithContext(ctx).Where("order_no = ?", paid.OrderNo).Take(&raced).Error
		if lookupErr == nil {
			return reservationFromRow(raced, paid, orderBCD, mode, quantity)
		}
	}
	return reservation, err
}

// reservationFromRow 接收已存 row、paid 身份及 orderBCD、mode、quantity，核对身份和两份六字节会话后还原启动及补偿命令；失配返回 ErrOrderConflict，无 IO。
func reservationFromRow(row chargeCommandRow, paid PaidOrder, orderBCD [8]byte, mode uint8, quantity uint16) (StartReservation, error) {
	if row.ChargeOrderID != paid.ChargeOrderID || row.PaymentOrderID != paid.PaymentOrderID || row.UserID != paid.UserID || row.DeviceID != paid.DeviceID || row.PortNo != paid.PortNo || row.ChargeMode != mode || row.Quantity != quantity || row.ConsumerType != paid.ConsumerType || row.CardNumber != paid.CardNumber || row.CardBalanceUnits != paid.CardBalanceUnits {
		return StartReservation{}, ErrOrderConflict
	}
	var session [6]byte
	if !row.SessionID.Valid {
		return StartReservation{}, ErrOrderConflict
	}
	value, err := hex.DecodeString(row.SessionID.String)
	if err != nil || len(value) != 6 {
		return StartReservation{}, ErrOrderConflict
	}
	copy(session[:], value)
	if !row.StopSessionID.Valid {
		return StartReservation{}, ErrOrderConflict
	}
	stopValue, err := hex.DecodeString(row.StopSessionID.String)
	if err != nil || len(stopValue) != 6 {
		return StartReservation{}, ErrOrderConflict
	}
	var stopSession [6]byte
	copy(stopSession[:], stopValue)
	return StartReservation{CommandID: row.CommandID, Status: row.Status, OrderNo: paid.OrderNo,
		DeviceID: paid.DeviceID, PortNo: paid.PortNo,
		Wire:     protocol.Command{Kind: protocol.CommandStart, SessionID: session, Port: paid.PortNo, OrderBCD: orderBCD, Mode: mode, Quantity: quantity, ConsumerType: paid.ConsumerType, CardNumber: paid.CardNumber, CardBalanceUnits: paid.CardBalanceUnits},
		StopWire: protocol.Command{Kind: protocol.CommandStop, SessionID: stopSession, Port: paid.PortNo}}, nil
}

// MarkStartSent 接收 ctx 与 commandID，仅将 pending 命令推进为 sent 并记录发送时间；返回是否影响一行及 SQL 错误，不发送设备命令。
func (s MySQLSink) MarkStartSent(ctx context.Context, commandID string) (bool, error) {
	result := s.DB.WithContext(ctx).Model(&chargeCommandRow{}).Where("command_id = ? AND status = 'pending'", commandID).
		Updates(map[string]any{"status": "sent", "sent_at": gorm.Expr("CURRENT_TIMESTAMP(3)")})
	return result.RowsAffected == 1, result.Error
}

// StartStatus 接收 ctx 与 orderNo，读取原启动命令状态，返回状态或记录缺失、SQL 错误，无写入。
func (s MySQLSink) StartStatus(ctx context.Context, orderNo string) (string, error) {
	var row chargeCommandRow
	if err := s.DB.WithContext(ctx).Select("status").Where("order_no = ?", orderNo).Take(&row).Error; err != nil {
		return "", err
	}
	return row.Status, nil
}

// MarkStartStopping 接收 ctx、commandID 与 cause，仅将 sent 或 acked 推进为 stopping，持久化最多 255 字节的原错误文本；返回 SQL 错误，不释放端口或重建会话。
func (s MySQLSink) MarkStartStopping(ctx context.Context, commandID string, cause error) error {
	message := "uncertain TCP write"
	if cause != nil {
		message = cause.Error()
	}
	if len(message) > 255 {
		message = message[:255]
	}
	return s.DB.WithContext(ctx).Model(&chargeCommandRow{}).Where("command_id = ? AND status IN ('sent','acked')", commandID).
		Updates(map[string]any{"status": "stopping", "error": message}).Error
}

// numericOrderBCD 接收 orderID，左补零转换为八字节 BCD；超过十六位返回 ErrOrderConflict，无 IO。
func numericOrderBCD(orderID uint64) ([8]byte, error) {
	var result [8]byte
	value := strconv.FormatUint(orderID, 10)
	if len(value) > 16 {
		return result, ErrOrderConflict
	}
	value = strings.Repeat("0", 16-len(value)) + value
	for i := range result {
		a, b := value[i*2], value[i*2+1]
		result[i] = (a-'0')<<4 | (b - '0')
	}
	return result, nil
}
