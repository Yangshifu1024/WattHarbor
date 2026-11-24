package repo

import (
	"context"
	"strings"
	"time"
	"wattharbor/internal/domain/money"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"wattharbor/internal/protocol"
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
func insertChargeProcess(ctx context.Context, tx *gorm.DB, key string, event protocol.Event) ([]chargeProcessRow, error) {
	if event.Protocol != protocol.AdapterIDDC589 || len(event.ChargingPorts) == 0 {
		return nil, nil
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
		return nil, err
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
		return nil, err
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
		return nil, nil
	}
	return rows, tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

// ListChargeProcessPoints 按 ID 升序查询订单采样，多取一条判断是否有下一页。
// 功率换算为 W、电量换算为 kWh；可选设备和端口参数进一步限制查询范围。
func ListChargeProcessPoints(
	ctx context.Context,
	db *gorm.DB,
	orderNo string,
	chargeOrderID uint64,
	deviceID string,
	portNo uint64,
	afterID uint64,
	limit int,
) ([]ChargeProcessPoint, error) {
	query := db.WithContext(ctx).Table("charge_process").
		Select("id,ts,power_deciwatts/10.0 AS power_w,"+
			"charged_mwh/1000000.0 AS charged_kwh,remaining_mwh/1000000.0 AS remaining_kwh,"+
			"charged_seconds,remaining_seconds,signal_strength,port_status,"+
			"voltage_v,temperature_c,device_status").
		Where("order_no=? AND id>?", orderNo, afterID)
	if chargeOrderID > 0 {
		query = query.Where("charge_order_id=?", chargeOrderID)
	}
	if deviceID != "" {
		query = query.Where("device_id=?", deviceID)
	}
	if portNo > 0 {
		query = query.Where("port_no=?", portNo)
	}
	rows := []ChargeProcessPoint{}
	err := query.Order("id ASC").Limit(limit + 1).Scan(&rows).Error
	return rows, err
}

// ChargeProcessPoint 返回订单采样，功率单位 W、电量单位 kWh，未上报扩展字段为 nil。
type ChargeProcessPoint struct {
	// ID charge_process 表的自增主键，用作过程曲线游标。
	ID uint64 `json:"id"`
	// TS 采样时刻，使用该结构的时间格式。
	TS time.Time `json:"ts"`
	// PowerW 转换后的功率，单位瓦。
	PowerW float64 `json:"power_w"`
	// ChargedKWh 转换后的已充电量，单位千瓦时。
	ChargedKWh float64 `json:"charged_kwh" gorm:"column:charged_kwh"`
	// RemainingKWh 转换后的剩余电量，单位千瓦时。
	RemainingKWh float64 `json:"remaining_kwh" gorm:"column:remaining_kwh"`
	// ChargedSeconds 已充时长秒数。
	ChargedSeconds uint32 `json:"charged_seconds"`
	// RemainingSeconds 剩余时长秒数。
	RemainingSeconds uint32 `json:"remaining_seconds"`
	// SignalStrength 设备信号强度整数，零为有效报告，不具有 NULL 标志。
	SignalStrength uint8 `json:"signal_strength"`
	// PortStatus 是端口状态，nil 表示未上报。
	PortStatus *uint8 `json:"port_status"`
	// VoltageV 是整机电压，单位 V；nil 表示未上报。
	VoltageV *uint16 `json:"voltage_v"`
	// TemperatureC 是整机温度，单位摄氏度；nil 表示未上报。
	TemperatureC *int16 `json:"temperature_c"`
	// DeviceStatus 设备协议状态，保留可空扩展块。
	DeviceStatus *uint8 `json:"device_status"`
}
