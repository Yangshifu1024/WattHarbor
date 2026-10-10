package money

// ScanPort 保存扫码结果中的端口编号、设备状态和可用性。
type ScanPort struct {
	PortID       string     `json:"port_id"`
	DeviceID     string     `json:"device_id"`
	PortNo       uint8      `json:"port_no"`
	Status       PortStatus `json:"port_status"`
	Online       bool       `json:"online"`
	Available    bool       `json:"available"`
	DeviceStatus string     `json:"device_status"`
}

// ScanResult 保存扫码解析结果；Kind 区分设备和端口，Port 或 Ports 保存对应端口信息。
type ScanResult struct {
	Kind         ScanKind   `json:"kind"`
	DeviceID     string     `json:"device_id"`
	StationID    uint64     `json:"station_id"`
	DeviceStatus string     `json:"device_status"`
	Port         *ScanPort  `json:"port,omitempty"`
	Ports        []ScanPort `json:"ports,omitempty"`
}
