package repo

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
)

// ScanPort 保存扫描端口编码、设备端口及原业务状态与在线可用读模型，不持有充电受理事实。
type ScanPort struct {
	// PortID 原二维码端口编码，对外字段保持 port_id 文本，不是 device_port 自增 id。
	PortID string `json:"port_id"`
	// DeviceID 原设备业务编号。
	DeviceID string `json:"device_id"`
	// PortNo 原协议端口序号。
	PortNo uint8 `json:"port_no"`
	// Status 所属记录的原状态值。
	Status string `json:"port_status"`
	// Online 按原可见时间窗口判断的在线状态。
	Online bool `json:"online"`
	// Available 原空闲、无订单占用且在线的可用状态。
	Available bool `json:"available"`
}

// ScanResult 保存扫描到的原端口或设备及所属站点读模型，单端口与端口集合保持原空值语义。
type ScanResult struct {
	// Kind 原扫描对象种类，port 或 device。
	Kind string `json:"kind"`
	// DeviceID 原设备业务编号。
	DeviceID string `json:"device_id"`
	// StationID 原所属站点身份，保留可空映射。
	StationID uint64 `json:"station_id"`
	// Port 为命中端口编码时的单端口读模型；按设备号命中时保留 nil。
	Port *ScanPort `json:"port,omitempty"`
	// Ports 设备端口记录，保留原集合空值语义。
	Ports []ScanPort `json:"ports,omitempty"`
}

var ErrScanNotFound = errors.New("scan code not found")

// ResolveScan 接收 ctx 与 code，先按启用端口编码查询，再按启用设备号读取端口列表；返回扫描读模型或 ErrScanNotFound、SQL 错误，不创建命令。
func (s MySQLSink) ResolveScan(ctx context.Context, code string) (ScanResult, error) {
	var port scanPortRow
	err := s.DB.WithContext(ctx).Table("device_port AS p").
		Select("p.id, p.device_id, p.port_no, p.port_code, p.status, p.current_order_id, d.last_seen_at, d.station_id").
		Joins("JOIN device AS d ON d.device_id = p.device_id").Joins("JOIN vendor AS v ON v.id = d.vendor_id").
		Where("p.port_code = ? AND p.deleted_at IS NULL AND d.status = 'enabled' AND d.deleted_at IS NULL AND v.status = 'enabled' AND v.deleted_at IS NULL", code).
		Take(&port).Error
	if err == nil {
		view := scanPort(port.PortCode, port.DeviceID, port.PortNo, port.Status, port.CurrentOrderID, port.LastSeenAt)
		return ScanResult{Kind: "port", DeviceID: view.DeviceID, StationID: uint64(port.StationID.Int64), Port: &view}, nil
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

// scanPortRow 投影 device_port 与 device 联表读取的原端口编码、占用、设备可见时刻和站点身份。
type scanPortRow struct {
	// ID device_port 表的原自增主键。
	ID uint64 `gorm:"column:id"`
	// DeviceID 原设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// PortNo 原协议端口序号。
	PortNo uint8 `gorm:"column:port_no"`
	// PortCode 原端口业务编码。
	PortCode string `gorm:"column:port_code"`
	// Status 所属记录的原状态值。
	Status string `gorm:"column:status"`
	// CurrentOrderID 原占用端口的订单号，NULL 表示无占用。
	CurrentOrderID sql.NullString `gorm:"column:current_order_id"`
	// LastSeenAt 原设备最后可见时刻，可空。
	LastSeenAt sql.NullTime `gorm:"column:last_seen_at"`
	// StationID 原所属站点身份，保留可空映射。
	StationID sql.NullInt64 `gorm:"column:station_id"`
}

// scanPort 接收 code、deviceID、number、status、current 与 seen，按原两分钟过去及一分钟未来窗口判断在线并计算空闲可用性；返回读模型，无 IO。
func scanPort(code, deviceID string, number uint8, status string, current sql.NullString, seen sql.NullTime) ScanPort {
	online := seen.Valid && time.Since(seen.Time) <= 2*time.Minute && time.Since(seen.Time) >= -time.Minute
	return ScanPort{PortID: code, DeviceID: deviceID, PortNo: number, Status: string(status), Online: online,
		Available: status == "idle" && !current.Valid && online}
}
