package repo

import (
	"context"
	"strings"
	"time"

	"chargepilot/internal/protocol"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// recordHeartbeatState 接收 ctx、所属 tx 和心跳 event，仅更新更晚的信号与已提供的端口上报状态；返回 SQL 错误，缺扩展块不清除已知状态，不更改业务端口占用。
func recordHeartbeatState(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	at := event.ReceivedAt.UTC().Truncate(time.Millisecond)
	if err := tx.WithContext(ctx).Model(&deviceRow{}).
		Where("device_id = ? AND deleted_at IS NULL AND (signal_at IS NULL OR signal_at < ?)", event.DeviceID, at).
		Updates(map[string]any{"signal_strength": event.Signal, "signal_at": at}).Error; err != nil {
		return err
	}
	if len(event.PortStates) == 0 {
		return nil
	}
	var state strings.Builder
	state.WriteString("CASE port_no")
	args := make([]any, 0, len(event.PortStates)*2)
	ports := make([]int, 0, len(event.PortStates))
	for i, status := range event.PortStates {
		port := uint8(i + 1)
		ports = append(ports, int(port))
		state.WriteString(" WHEN ? THEN ?")
		args = append(args, port, status)
	}
	state.WriteString(" END")
	return tx.WithContext(ctx).Model(&devicePortRow{}).
		Where("device_id = ? AND port_no IN ? AND deleted_at IS NULL AND (reported_status_at IS NULL OR reported_status_at < ?)", event.DeviceID, ports, at).
		Updates(map[string]any{"reported_status": gorm.Expr(state.String(), args...), "reported_status_at": at}).Error
}

// chargeProcessRow 映射 charge_process 的原持久充电采样，保留订单关联、整数单位及可空协议扩展证据。
type chargeProcessRow struct {
	// ID charge_process 表的原自增主键。
	ID uint64 `gorm:"primaryKey"`
	// EventKey 原持久事件幂等键。
	EventKey string
	// ChargeOrderID 中央充电订单身份。
	ChargeOrderID uint64
	// OrderNo 原业务订单号。
	OrderNo string
	// DeviceID 原设备业务编号。
	DeviceID string
	// PortNo 原协议端口序号。
	PortNo uint8
	// TS 原采样时刻，保留所属结构的时间表示。
	TS time.Time
	// PowerDeciwatts 原功率整数，单位十分之一瓦。
	PowerDeciwatts uint32
	// ChargedSeconds 原已充时长秒数。
	ChargedSeconds uint32
	// RemainingSeconds 原剩余时长秒数。
	RemainingSeconds uint32
	// ChargedMWh 原已充电量整数，单位毫瓦时。
	ChargedMWh uint32 `gorm:"column:charged_mwh"`
	// RemainingMWh 原剩余电量整数，单位毫瓦时。
	RemainingMWh uint32 `gorm:"column:remaining_mwh"`
	// SignalStrength 原设备信号强度，保留未知可空映射。
	SignalStrength uint8
	// PortStatus 原端口协议状态，nil 表示未提供该证据。
	PortStatus *uint8
	// VoltageV 原设备电压值，保留可空扩展块。
	VoltageV *uint16
	// TemperatureC 原设备温度值，保留可空扩展块。
	TemperatureC *int16
	// DeviceStatus 原设备协议状态，保留可空扩展块。
	DeviceStatus *uint8
}

// TableName 返回原固定表名 charge_process，供 GORM 定位模型；无数据库 IO。
func (chargeProcessRow) TableName() string { return "charge_process" }

// insertChargeProcess 接收 ctx、所属 tx、事件 key 和 event，锁定端口后关联已确认且确认时间不晚于上报的原订单，幂等插入过程点；不匹配返回 nil，SQL 失败返回错误，不提交事务。
func insertChargeProcess(ctx context.Context, tx *gorm.DB, key string, event protocol.Event) error {
	if event.Protocol != "dc589" || len(event.ChargingPorts) == 0 {
		return nil
	}
	ports := make([]int, 0, len(event.ChargingPorts))
	for _, port := range event.ChargingPorts {
		ports = append(ports, int(port.Port))
	}
	at := event.ReceivedAt.UTC().Truncate(time.Millisecond)
	// 保存过程记录前锁定端口，保持订单归属不变。
	// 启动确认按命令、端口顺序加锁；此处仅锁端口，避免反向锁定命令形成死锁。
	var locked []devicePortRow
	if err := tx.WithContext(ctx).Select("id").
		Where("device_id=? AND port_no IN ? AND deleted_at IS NULL", event.DeviceID, ports).
		Order("port_no").Clauses(clause.Locking{Strength: "UPDATE"}).Find(&locked).Error; err != nil {
		return err
	}
	var sessions []struct {
		// ChargeOrderID 中央充电订单身份。
		ChargeOrderID uint64
		// OrderNo 原业务订单号。
		OrderNo string
		// PortNo 原协议端口序号。
		PortNo uint8
	}
	if err := tx.WithContext(ctx).Table("device_port AS p").
		Select("c.charge_order_id,c.order_no,p.port_no").
		Joins("JOIN charge_command AS c ON c.order_no=p.current_order_id AND c.device_id=p.device_id AND c.port_no=p.port_no AND c.port_id=p.id").
		Where("p.device_id=? AND p.port_no IN ? AND p.deleted_at IS NULL AND p.status='charging' AND c.status='acked' AND c.ack_at<=?", event.DeviceID, ports, at).
		Scan(&sessions).Error; err != nil {
		return err
	}
	byPort := make(map[uint8]int, len(sessions))
	for i, session := range sessions {
		byPort[session.PortNo] = i
	}
	rows := make([]chargeProcessRow, 0, len(sessions))
	for _, port := range event.ChargingPorts {
		i, ok := byPort[port.Port]
		if !ok {
			continue
		}
		session := sessions[i]
		row := chargeProcessRow{EventKey: key, ChargeOrderID: session.ChargeOrderID, OrderNo: session.OrderNo,
			DeviceID: event.DeviceID, PortNo: port.Port, TS: at, PowerDeciwatts: port.PowerDeciWatts,
			ChargedSeconds: port.ChargedSeconds, RemainingSeconds: port.RemainingSecs,
			ChargedMWh: port.ChargedMWh, RemainingMWh: port.RemainingMWh, SignalStrength: event.Signal}
		if event.PortStates != nil {
			row.VoltageV, row.TemperatureC, row.DeviceStatus = &event.VoltageV, &event.TemperatureC, &event.DeviceStatus
			if port.Port > 0 && int(port.Port) <= len(event.PortStates) {
				row.PortStatus = &event.PortStates[port.Port-1]
			}
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}
