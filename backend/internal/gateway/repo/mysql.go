// Package repo 查询和更新网关的设备、端口、命令、事件及遥测表。
package repo

import (
	"chargepilot/internal/domain/devices"
	"chargepilot/internal/domain/money"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"chargepilot/internal/protocol"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrDeviceNotProvisioned 设备尚未开通，或所用协议与厂商配置不一致。
var ErrDeviceNotProvisioned = errors.New("device is not provisioned for this protocol")

// MySQLSink 保存设备事件、连接记录和命令结果，并提供网关查询。
// 数据库连接由进程管理，各方法按需要使用事务。
type MySQLSink struct {
	// DB 是网关数据库连接。
	DB *gorm.DB
}

// HasChargingPorts 查询未删除且 charging 的端口是否存在，返回布尔值和 SQL 错误，不写库。
func (s MySQLSink) HasChargingPorts(ctx context.Context, deviceID string) (bool, error) {
	var count int64
	err := s.DB.WithContext(ctx).Model(&devicePortRow{}).Where("device_id=? AND status=? AND deleted_at IS NULL", deviceID, money.PortCharging).Count(&count).Error
	return count > 0, err
}

// cardDeliveryRow 仅映射 card_event_delivery 事件键，供持久设备卡事件时幂等建立投递记录。
// status 列不在此处映射：初值由数据库 default pending 填充，投递状态只由
// AdvanceCardDelivery 显式更新，读取侧走 ListPendingCardDeliveries 的 SQL 条件。
type cardDeliveryRow struct {
	// EventKey 持久事件幂等键。
	EventKey string `gorm:"column:event_key;primaryKey"`
}

// TableName 返回固定表名 card_event_delivery，供 GORM 定位模型。
func (cardDeliveryRow) TableName() string { return "card_event_delivery" }

// Register 接收 ctx 与设备注册信息，核对启用设备及厂商适配器后写入固件与登记时间；缺配置、未开通或 SQL 失败返回错误，不发送协议报文。
func (s MySQLSink) Register(ctx context.Context, registration protocol.Registration) error {
	if s.DB == nil {
		return errors.New("gateway database is unavailable")
	}
	var device deviceRow
	// 按厂商 adapter_class 校验协议匹配；vendor_code 为业务标识，不代表设备协议。
	err := s.DB.WithContext(ctx).Table("device AS d").Select("d.id").
		Joins("JOIN vendor AS v ON v.id = d.vendor_id").
		Where("d.device_id = ? AND d.status = 'enabled' AND d.deleted_at IS NULL AND v.adapter_class = ? AND v.status = 'enabled' AND v.deleted_at IS NULL", registration.DeviceID, registration.Protocol).
		Take(&device).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrDeviceNotProvisioned
	}
	if err != nil {
		return fmt.Errorf("verify device: %w", err)
	}
	now := registration.ReceivedAt.UTC()
	result := s.DB.WithContext(ctx).Model(&deviceRow{}).Where("id = ?", device.ID).Updates(map[string]any{
		"firmware_version": registration.SoftwareVersion,
		"registered_at":    gorm.Expr("COALESCE(registered_at, ?)", now),
		"last_seen_at":     now,
	})
	if result.Error != nil {
		return fmt.Errorf("record device registration: %w", result.Error)
	}
	return nil
}

// Record 在 READ COMMITTED 事务内保存事件、Outbox、命令回执和遥测；提交成功后由适配器确认设备帧。
func (s MySQLSink) Record(ctx context.Context, event protocol.Event) error {
	if s.DB == nil {
		return errors.New("gateway database is unavailable")
	}
	if (event.Type == protocol.CardSwipe || event.Type == protocol.CardBalanceQuery) && (uuid.Validate(event.EventID) != nil || event.Type == protocol.CardSwipe && event.CardNumber == 0) {
		return errors.New("card event requires a durable UUID and card number")
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal device event: %w", err)
	}
	key := eventKey(event)
	// Outbox 使用统一事件结构：event_id、event_type、source、occurred_at 和 data。
	// 调用方按事件字段路由；device_event.event_json 另行保留解码事件及原始载荷，供诊断和重放。
	envelope, err := json.Marshal(map[string]any{
		"event_id": key,
		// event_type 取自 protocol.EventType（device_event.event_type 同一词表）；
		// 显式转换只为 map 落普通字符串，编码后的 JSON 字节不变。
		"event_type":  string(event.Type),
		"source":      "gateway",
		"occurred_at": event.ReceivedAt.UTC(),
		"data":        json.RawMessage(data),
	})
	if err != nil {
		return fmt.Errorf("marshal device event envelope: %w", err)
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&deviceEventRow{
			EventKey: key, Protocol: event.Protocol, DeviceID: event.DeviceID,
			EventType: event.Type, PortNo: event.Port, EventJSON: data, ReceivedAt: event.ReceivedAt.UTC(),
		})
		if inserted.Error != nil {
			return fmt.Errorf("persist device event: %w", inserted.Error)
		}
		if event.Type == protocol.CardSwipe || event.Type == protocol.CardBalanceQuery {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&cardDeliveryRow{EventKey: key}).Error; err != nil {
				return err
			}
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&deviceOutboxRow{EventID: key, Stream: "device_event_stream", EnvelopeJSON: envelope}).Error; err != nil {
			return fmt.Errorf("queue device event: %w", err)
		}
		if inserted.RowsAffected == 1 && event.Type == protocol.StartResult {
			if err := applyStartAck(ctx, tx, event); err != nil {
				return fmt.Errorf("apply start acknowledgement: %w", err)
			}
		}
		if inserted.RowsAffected == 1 && event.Type == protocol.StopResult {
			if err := applyStopAck(ctx, tx, event); err != nil {
				return fmt.Errorf("apply stop acknowledgement: %w", err)
			}
			if err := applyUserStopAck(ctx, tx, event); err != nil {
				return fmt.Errorf("apply user stop acknowledgement: %w", err)
			}
		}
		if event.Type == protocol.Heartbeat {
			if err := tx.Model(&deviceRow{}).Where("device_id = ? AND deleted_at IS NULL", event.DeviceID).
				Updates(map[string]any{"last_seen_at": gorm.Expr("GREATEST(COALESCE(last_seen_at, ?), ?)", event.ReceivedAt.UTC(), event.ReceivedAt.UTC()), "last_heartbeat_at": gorm.Expr("GREATEST(COALESCE(last_heartbeat_at, ?), ?)", event.ReceivedAt.UTC(), event.ReceivedAt.UTC())}).Error; err != nil {
				return fmt.Errorf("touch device heartbeat: %w", err)
			}
		}
		if inserted.RowsAffected == 1 {
			if event.Type == protocol.Heartbeat {
				if err := recordHeartbeatState(ctx, tx, event); err != nil {
					return fmt.Errorf("persist heartbeat state: %w", err)
				}
				if err := insertChargeProcess(ctx, tx, key, event); err != nil {
					return fmt.Errorf("persist charge process: %w", err)
				}
			}
			if err := insertMeasurements(ctx, tx, event); err != nil {
				return fmt.Errorf("persist device telemetry: %w", err)
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

// stopAckAccepted 识别已停止输出（0x10）或已空闲（0x01）的设备回执。
func stopAckAccepted(code uint8) bool { return code == 0x10 || code == 0x01 }

// applyUserStopAck 按设备、端口和会话锁定用户停机命令，保存确认或拒绝结果；无匹配命令则跳过。
func applyUserStopAck(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	var command chargeStopCommandRow
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id = ? AND port_no = ? AND session_id = ?", event.DeviceID, event.Port, hex.EncodeToString(event.SessionID[:])).Take(&command).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if stopAckAccepted(event.ResultCode) {
		// SQL 内 status = 'sent'／status = 'acked' 分别对应
		// devices.StopCommandSent／StopCommandSent／StopCommandAcked（charge_stop_command.status）。
		if command.Status == devices.StopCommandSent {
			return tx.Model(&chargeStopCommandRow{}).Where("command_id = ? AND status = 'sent'", command.CommandID).
				Updates(map[string]any{"status": devices.StopCommandAcked, "result_code": int16(event.ResultCode)}).Error
		}
		return nil
	}
	// 设备拒绝停止时持久化失败结果，避免命令持续停留在 sent 状态。
	if command.Status == devices.StopCommandSent {
		return tx.Model(&chargeStopCommandRow{}).Where("command_id = ? AND status = 'sent'", command.CommandID).
			Updates(map[string]any{
				"status":      devices.StopCommandRejected,
				"result_code": int16(event.ResultCode),
				"rejected_at": event.ReceivedAt.UTC(),
			}).Error
	}
	return nil
}

// applyStartAck 锁定匹配的启动命令，更新确认或拒绝状态及端口占用；无匹配命令则跳过。
func applyStartAck(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	var command chargeCommandRow
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id = ? AND port_no = ? AND session_id = ?", event.DeviceID, event.Port, hex.EncodeToString(event.SessionID[:])).Take(&command).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 保留配不上的 ACK 供事后排查；它们无法改变任何订单。
		return nil
	}
	if err != nil {
		return err
	}
	if !command.PortID.Valid || command.PortID.Int64 <= 0 {
		return ErrOrderConflict
	}
	if event.ResultCode == 0 && command.Status == devices.CommandSent {
		// SQL 内 status = 'sent'／status = 'acked' 对应 devices.CommandSent／CommandAcked
		// （charge_command.status）。
		changed := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status = 'sent'", command.CommandID).
			Updates(map[string]any{"status": devices.CommandAcked, "error": nil, "result_code": 0, "ack_at": event.ReceivedAt.UTC()})
		if err := requireOne(changed); err != nil {
			return ErrOrderConflict
		}
		port := tx.Model(&devicePortRow{}).Where("id = ? AND current_order_id = ? AND status = ?", command.PortID.Int64, command.OrderNo, money.PortIdle).Update("status", money.PortCharging)
		if err := requireOne(port); err != nil {
			return ErrPortUnavailable
		}
	}
	if event.ResultCode != 0 && (command.Status == devices.CommandSent || command.Status == devices.CommandStopping) {
		// SQL 内 status IN ('sent','stopping') 对应 devices.CommandSent、CommandStopping。
		updated := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status IN ('sent','stopping')", command.CommandID).
			Updates(map[string]any{"status": devices.CommandRejected, "error": fmt.Sprintf("device rejected START code %d", event.ResultCode), "result_code": event.ResultCode, "ack_at": event.ReceivedAt.UTC()})
		if updated.Error != nil {
			return updated.Error
		}
		return tx.Model(&devicePortRow{}).Where("id = ? AND current_order_id = ? AND status = ?", command.PortID.Int64, command.OrderNo, money.PortIdle).
			Update("current_order_id", nil).Error
	}
	return nil
}

// applyStopAck 只处理 stopping 命令的停止终态回执，拒绝启动并释放该订单占用的端口。
func applyStopAck(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	var command chargeCommandRow
	// SQL 内 status = 'stopping' 对应 devices.CommandStopping（charge_command.status）。
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id = ? AND port_no = ? AND stop_session_id = ? AND status = 'stopping'", event.DeviceID, event.Port, hex.EncodeToString(event.SessionID[:])).Take(&command).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	// 0x10 表示停止输出，0x01 表示端口已空闲。
	// 设备拒绝停止时保留 stopping 状态，不提前释放端口。
	if !stopAckAccepted(event.ResultCode) {
		return nil
	}
	if !command.PortID.Valid || command.PortID.Int64 <= 0 {
		return ErrOrderConflict
	}
	changed := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status = 'stopping'", command.CommandID).
		Updates(map[string]any{"status": devices.CommandRejected, "result_code": 254, "ack_at": event.ReceivedAt.UTC(), "error": "STOP compensation confirmed"})
	if err := requireOne(changed); err != nil {
		return ErrOrderConflict
	}
	port := tx.Model(&devicePortRow{}).Where("id = ? AND current_order_id = ? AND status IN ?", command.PortID.Int64, command.OrderNo, idleOrChargingPorts()).
		Updates(map[string]any{"status": money.PortIdle, "current_order_id": nil})
	if err := requireOne(port); err != nil {
		return ErrPortUnavailable
	}
	return nil
}

// insertMeasurements 在调用方事务内转换协议单位，保存遥测并更新两类汇总。
func insertMeasurements(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	if event.Type != protocol.Heartbeat && event.Type != protocol.ChargeEnd && len(event.ChargingPorts) == 0 {
		return nil
	}
	// 与补传共用设备行锁，避免两条写入链在预查与插入之间抢占相同样本。
	if _, err := deviceStatus(ctx, tx, event.DeviceID); err != nil {
		return err
	}
	// 收集本批样本，按汇总粒度批量更新，避免逐指标执行 SQL。
	rollup := make([]AggregateSample, 0, 8)
	insert := func(port sql.NullInt16, metric, value string) error {
		at := event.ReceivedAt.UTC().Truncate(time.Millisecond)
		inserted := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
			Create(&telemetryRow{DeviceID: event.DeviceID, PortNo: port, Metric: metric, ValueNum: value, TS: at})
		if inserted.Error != nil {
			return inserted.Error
		}
		// 重复样本保留首次读数，只有新插入的样本才计入汇总。
		if inserted.RowsAffected == 1 {
			rollup = append(rollup, AggregateSample{DeviceID: event.DeviceID, Port: port, Metric: metric, Value: value, TS: at})
		}
		return nil
	}
	if event.Type == protocol.Heartbeat {
		if err := insert(sql.NullInt16{}, "signal", decimal.NewFromInt(int64(event.Signal)).String()); err != nil {
			return err
		}
		if event.PortStates != nil {
			if err := insert(sql.NullInt16{}, "voltage_v", decimal.NewFromInt(int64(event.VoltageV)).String()); err != nil {
				return err
			}
			if err := insert(sql.NullInt16{}, "temperature_c", decimal.NewFromInt(int64(event.TemperatureC)).String()); err != nil {
				return err
			}
		}
	}
	for _, port := range event.ChargingPorts {
		id := sql.NullInt16{Int16: int16(port.Port), Valid: true}
		if err := insert(id, "meter_kwh", decimal.NewFromInt(int64(port.ChargedMWh)).Shift(-6).StringFixed(6)); err != nil {
			return err
		}
		if err := insert(id, "power_w", decimal.NewFromInt(int64(port.PowerDeciWatts)).Shift(-1).StringFixed(1)); err != nil {
			return err
		}
	}
	if event.Type == protocol.ChargeEnd {
		id := sql.NullInt16{Int16: int16(event.Port), Valid: true}
		if err := insert(id, "meter_kwh", decimal.NewFromInt(int64(event.EnergyMilliKWh)).Shift(-3).StringFixed(3)); err != nil {
			return err
		}
	}
	return refreshAggregates(ctx, tx, rollup)
}

// requireOne 要求恰好更新一行；SQL 失败或行数不符时返回错误。
func requireOne(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// eventKey 生成事件去重键：刷卡复用 UUID，心跳和故障包含接收时间。
// 独立遥测每次生成新 UUID，其余事件按设备、会话和原始载荷计算摘要。
func eventKey(event protocol.Event) string {
	if (event.Type == protocol.CardSwipe || event.Type == protocol.CardBalanceQuery) && uuid.Validate(event.EventID) == nil {
		return event.EventID
	}
	if event.Type == protocol.Telemetry {
		return uuid.NewString()
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(event.Protocol))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(event.DeviceID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(event.Type))
	_, _ = hash.Write(event.SessionID[:])
	_, _ = hash.Write(event.RawPayload)
	if event.Type == protocol.Heartbeat {
		// 同一次接收的心跳重放只保存一次；相同报文在之后的接收仍是新的采样。
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(event.ReceivedAt.UTC().Format(time.RFC3339Nano)))
		if len(event.RawPayload) == 0 {
			// 结构化事件重放没有原始帧时，也须保留同一时刻不同测量的区别。
			normalized := event
			normalized.ReceivedAt = event.ReceivedAt.UTC()
			normalized.StartedAt = event.StartedAt.UTC()
			normalized.EndedAt = event.EndedAt.UTC()
			data, _ := json.Marshal(normalized)
			_, _ = hash.Write(data)
		}
	}
	if event.Type == protocol.Fault {
		// C0 没有故障序号；相同报文可以在恢复后再次出现。
		// 每次接收都保存；持续故障是否重复告警由告警服务判断。
		_, _ = hash.Write([]byte(event.ReceivedAt.UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// idleOrChargingPorts 返回可被释放的端口状态集合：空闲与充电中的端口都可以结束
// 占用回落到空闲，full、fault 与 disabled 不在集合内，避免释放不可用端口。
func idleOrChargingPorts() []money.PortStatus {
	return []money.PortStatus{money.PortIdle, money.PortCharging}
}
