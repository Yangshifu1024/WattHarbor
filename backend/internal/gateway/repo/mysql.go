package repo

import (
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

var ErrDeviceNotProvisioned = errors.New("device is not provisioned for this protocol")

// MySQLSink 实现设备注册、事件与连接终态持久能力，并提供网关命令和查询结果操作；连接池由装配方管理，事务边界以各方法约定为准。
type MySQLSink struct {
	// DB 装配方提供的 gateway GORM 连接句柄；连接池由资源装配方管理，事务边界以具体方法为准。
	DB *gorm.DB
}

// HasChargingPorts 查询未删除且 charging 的端口是否存在，返回布尔值和 SQL 错误，不写库。
func (s MySQLSink) HasChargingPorts(ctx context.Context, deviceID string) (bool, error) {
	var count int64
	err := s.DB.WithContext(ctx).Model(&devicePortRow{}).Where("device_id=? AND status='charging' AND deleted_at IS NULL", deviceID).Count(&count).Error
	return count > 0, err
}

// cardDeliveryRow 仅映射 card_event_delivery 事件键，供持久设备卡事件时幂等建立投递记录。
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
		"event_id":    key,
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
			EventType: string(event.Type), PortNo: event.Port, EventJSON: data, ReceivedAt: event.ReceivedAt.UTC(),
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

// stopAckAccepted 接收协议结果 code，仅 0x10 和 0x01 返回停止终态 true；其余返回 false。
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
		if command.Status == "sent" {
			return tx.Model(&chargeStopCommandRow{}).Where("command_id = ? AND status = 'sent'", command.CommandID).
				Updates(map[string]any{"status": "acked", "result_code": int16(event.ResultCode)}).Error
		}
		return nil
	}
	// 设备拒绝停止时持久化失败结果，避免命令持续停留在 sent 状态。
	if command.Status == "sent" {
		return tx.Model(&chargeStopCommandRow{}).Where("command_id = ? AND status = 'sent'", command.CommandID).
			Updates(map[string]any{
				"status":      "rejected",
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
	if event.ResultCode == 0 && command.Status == "sent" {
		changed := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status = 'sent'", command.CommandID).
			Updates(map[string]any{"status": "acked", "error": nil, "result_code": 0, "ack_at": event.ReceivedAt.UTC()})
		if err := requireOne(changed); err != nil {
			return ErrOrderConflict
		}
		port := tx.Model(&devicePortRow{}).Where("id = ? AND current_order_id = ? AND status = 'idle'", command.PortID.Int64, command.OrderNo).Update("status", "charging")
		if err := requireOne(port); err != nil {
			return ErrPortUnavailable
		}
	}
	if event.ResultCode != 0 && (command.Status == "sent" || command.Status == "stopping") {
		updated := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status IN ('sent','stopping')", command.CommandID).
			Updates(map[string]any{"status": "rejected", "error": fmt.Sprintf("device rejected START code %d", event.ResultCode), "result_code": event.ResultCode, "ack_at": event.ReceivedAt.UTC()})
		if updated.Error != nil {
			return updated.Error
		}
		return tx.Model(&devicePortRow{}).Where("id = ? AND current_order_id = ? AND status = 'idle'", command.PortID.Int64, command.OrderNo).
			Update("current_order_id", nil).Error
	}
	return nil
}

// applyStopAck 只处理 stopping 命令的停止终态回执，拒绝启动并释放该订单占用的端口。
func applyStopAck(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	var command chargeCommandRow
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
		Updates(map[string]any{"status": "rejected", "result_code": 254, "ack_at": event.ReceivedAt.UTC(), "error": "STOP compensation confirmed"})
	if err := requireOne(changed); err != nil {
		return ErrOrderConflict
	}
	port := tx.Model(&devicePortRow{}).Where("id = ? AND current_order_id = ? AND status IN ('idle','charging')", command.PortID.Int64, command.OrderNo).
		Updates(map[string]any{"status": "idle", "current_order_id": nil})
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
		// 唯一键冲突保持首次读数，只有新增事实才能累加聚合。
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

// requireOne 接收 GORM result，保留 SQL 错误，影响行数不为一时返回记录缺失，否则返回 nil。
func requireOne(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// eventKey 接收 event，刷卡复用有效 UUID、独立遥测生成新 UUID，其余按协议载荷及适用接收时刻生成摘要；返回事件键，不访问存储。
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
		// 每次接收独立持久化，持续故障的去重由告警状态处理。
		_, _ = hash.Write([]byte(event.ReceivedAt.UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
