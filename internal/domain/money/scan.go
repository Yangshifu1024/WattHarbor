package money

type ScanPort struct {
	PortID       string `json:"port_id"`
	DeviceID     string `json:"device_id"`
	PortNo       uint8  `json:"port_no"`
	Status       string `json:"port_status"`
	Online       bool   `json:"online"`
	Available    bool   `json:"available"`
	DeviceStatus string `json:"device_status"`
}

type ScanResult struct {
	Kind         string     `json:"kind"`
	DeviceID     string     `json:"device_id"`
	StationID    uint64     `json:"station_id"`
	DeviceStatus string     `json:"device_status"`
	Port         *ScanPort  `json:"port,omitempty"`
	Ports        []ScanPort `json:"ports,omitempty"`
}
