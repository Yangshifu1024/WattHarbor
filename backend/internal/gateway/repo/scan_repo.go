package repo

import (
	"context"
	"database/sql"
	"errors"
	"time"
	"wattharbor/internal/domain/money"

	"gorm.io/gorm"
)

// ScanPort 保存扫码结果中的设备、端口、业务状态和在线状态。
type ScanPort struct {
	// PortID 二维码端口编码，对外字段保持 port_id 文本，不是 device_port 自增 id。
	PortID string `json:"port_id"`
	// DeviceID 设备业务编号。
	DeviceID string `json:"device_id"`
	// PortNo 协议端口序号。
	PortNo uint8 `json:"port_no"`
	// Status 状态。
	Status money.PortStatus `json:"port_status"`
	// Online 按可见时间窗口判断的在线状态。
	Online bool `json:"online"`
	// Available 空闲、无订单占用且在线的可用状态。
	Available bool `json:"available"`
}

// ScanResult 返回扫描到的设备及端口；端口码命中时使用 Port，设备号命中时使用 Ports。
type ScanResult struct {
	// Kind 扫描对象种类，port 或 device。
	Kind money.ScanKind `json:"kind"`
	// DeviceID 设备业务编号。
	DeviceID string `json:"device_id"`
	// StationID 站点编号，保留可空映射。
	StationID uint64 `json:"station_id"`
	// Port 为命中端口编码时的单端口查询结果；按设备号命中时保留 nil。
	Port *ScanPort `json:"port,omitempty"`
	// Ports 是设备下的端口集合。
	Ports []ScanPort `json:"ports,omitempty"`
}

// ErrScanNotFound 扫描码未匹配到可用设备或端口。
var ErrScanNotFound = errors.New("scan code not found")

// ResolveScan 先按启用端口编码查询，再按启用设备号读取端口列表；返回扫描查询结果或 ErrScanNotFound、SQL 错误，不创建命令。
func (s MySQLSink) ResolveScan(ctx context.Context, code string) (ScanResult, error) {
	var port scanPortRow
	err := s.DB.WithContext(ctx).Table("device_port AS p").
		Select("p.id, p.device_id, p.port_no, p.port_code, p.status, p.current_order_id, d.last_seen_at, d.station_id").
		Joins("JOIN device AS d ON d.device_id = p.device_id").Joins("JOIN vendor AS v ON v.id = d.vendor_id").
		Where("p.port_code = ? AND p.deleted_at IS NULL AND d.status = 'enabled' AND d.deleted_at IS NULL AND v.status = 'enabled' AND v.deleted_at IS NULL", code).
		Take(&port).Error
	if err == nil {
		view := scanPort(port.PortCode, port.DeviceID, port.PortNo, port.Status, port.CurrentOrderID, port.LastSeenAt)
		return ScanResult{Kind: money.ScanKindPort, DeviceID: view.DeviceID, StationID: uint64(port.StationID.Int64), Port: &view}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return ScanResult{}, err
	}
	var rows []scanPortRow
	err = s.DB.WithContext(ctx).Table("device_port AS p").
		Select("p.id, p.device_id, p.port_no, p.port_code, p.status, p.current_order_id, d.last_seen_at, d.station_id").
		Joins("JOIN device AS d ON d.device_id = p.device_id").Joins("JOIN vendor AS v ON v.id = d.vendor_id").
		Where("d.device_id = ? AND p.deleted_at IS NULL AND d.status = 'enabled' AND d.deleted_at IS NULL AND v.status = 'enabled' AND v.deleted_at IS NULL", code).
		Order("p.port_no").Find(&rows).Error
	if err != nil {
		return ScanResult{}, err
	}
	if len(rows) == 0 {
		return ScanResult{}, ErrScanNotFound
	}
	result := ScanResult{Kind: "device", DeviceID: code, StationID: uint64(rows[0].StationID.Int64), Ports: make([]ScanPort, 0, len(rows))}
	for _, row := range rows {
		result.Ports = append(result.Ports, scanPort(row.PortCode, row.DeviceID, row.PortNo, row.Status, row.CurrentOrderID, row.LastSeenAt))
	}
	return result, nil
}

// scanPortRow 保存端口与设备联表查出的端口编码、占用、最近心跳时间和站点编号。
type scanPortRow struct {
	// ID device_port 表的自增主键。
	ID uint64 `gorm:"column:id"`
	// DeviceID 设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// PortNo 协议端口序号。
	PortNo uint8 `gorm:"column:port_no"`
	// PortCode 端口业务编码。
	PortCode string `gorm:"column:port_code"`
	// Status 状态。
	Status money.PortStatus `gorm:"column:status"`
	// CurrentOrderID 占用端口的订单号，NULL 表示无占用。
	CurrentOrderID sql.NullString `gorm:"column:current_order_id"`
	// LastSeenAt 设备最后可见时刻，可空。
	LastSeenAt sql.NullTime `gorm:"column:last_seen_at"`
	// StationID 站点编号，保留可空映射。
	StationID sql.NullInt64 `gorm:"column:station_id"`
}

// scanPort 允许心跳晚于当前时间最多 1 分钟、早于当前时间最多 2 分钟，并结合端口占用判断是否可用。
func scanPort(code, deviceID string, number uint8, status money.PortStatus, current sql.NullString, seen sql.NullTime) ScanPort {
	online := portDeviceOnline(seen, time.Now())
	return ScanPort{PortID: code, DeviceID: deviceID, PortNo: number, Status: status, Online: online,
		Available: portAvailable(status, current, online)}
}

// portDeviceOnline 使用最近可见时间判断扫码窗口，允许设备时钟最多快一分钟。
func portDeviceOnline(seen sql.NullTime, now time.Time) bool {
	age := now.Sub(seen.Time)
	return seen.Valid && age <= 2*time.Minute && age >= -time.Minute
}

// portAvailable 供扫码与列表摘要复用，仅在线、空闲且没有订单占用的端口可用。
func portAvailable(status money.PortStatus, current sql.NullString, online bool) bool {
	return online && status == money.PortIdle && !current.Valid
}
