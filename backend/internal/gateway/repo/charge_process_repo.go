package repo

import (
	"chargepilot/internal/domain/money"
	"context"
	"strings"
	"time"

	"chargepilot/internal/protocol"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// recordHeartbeatState 只更新较新心跳中已提供的信号和端口状态，不清除缺失信息或改变端口占用。
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

// chargeProcessRow 保存订单心跳采样，电量单位 mWh、功率单位 0.1 W。
type chargeProcessRow struct {
	// ID charge_process 表的自增主键。
	ID uint64 `gorm:"primaryKey"`
	// EventKey 持久事件幂等键。
	EventKey string
	// ChargeOrderID 中央充电订单编号。
	ChargeOrderID uint64
	// OrderNo 业务订单号。
	OrderNo string
	// DeviceID 设备业务编号。
	DeviceID string
	// PortNo 协议端口序号。
	PortNo uint8
	// TS 采样时刻，使用该结构的时间格式。
	TS time.Time
	// PowerDeciwatts 功率整数，单位十分之一瓦。
	PowerDeciwatts uint32
	// ChargedSeconds 已充时长秒数。
	ChargedSeconds uint32
	// RemainingSeconds 剩余时长秒数。
	RemainingSeconds uint32
	// ChargedMWh 已充电量整数，单位毫瓦时。
	ChargedMWh uint32 `gorm:"column:charged_mwh"`
	// RemainingMWh 剩余电量整数，单位毫瓦时。
	RemainingMWh uint32 `gorm:"column:remaining_mwh"`
	// SignalStrength 设备信号强度，保留未知可空映射。
	SignalStrength uint8
	// PortStatus 是上报端口状态，nil 表示未上报。
	PortStatus *uint8
	// VoltageV 设备电压值，保留可空扩展块。
	VoltageV *uint16
	// TemperatureC 设备温度值，保留可空扩展块。
	TemperatureC *int16
	// DeviceStatus 设备协议状态，保留可空扩展块。
	DeviceStatus *uint8
}

// TableName 返回固定表名 charge_process，供 GORM 定位模型。
func (chargeProcessRow) TableName() string { return "charge_process" }

// insertChargeProcess 在事务内锁定端口，将心跳关联到此前已确认启动的订单，并去重插入采样点。
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
		// ChargeOrderID 中央充电订单编号。
		ChargeOrderID uint64
		// OrderNo 业务订单号。
		OrderNo string
		// PortNo 协议端口序号。
		PortNo uint8
	}
	if err := tx.WithContext(ctx).Table("device_port AS p").
		Select("c.charge_order_id,c.order_no,p.port_no").
		Joins("JOIN charge_command AS c ON c.order_no=p.current_order_id AND c.device_id=p.device_id AND c.port_no=p.port_no AND c.port_id=p.id").
		Where("p.device_id=? AND p.port_no IN ? AND p.deleted_at IS NULL AND p.status=? AND c.status='acked' AND c.ack_at<=?", event.DeviceID, ports, money.PortCharging, at).
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
