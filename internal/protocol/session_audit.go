package protocol

import "time"

// Transport 记录 tcp 或 mqtt，与 device_session.protocol 枚举一致。
// 该字段表示传输方式，不填写 dc589 等设备协议名称。
type Transport string

const (
	TransportTCP  Transport = "tcp"
	TransportMQTT Transport = "mqtt"
)

// SessionRecord 是一条连接的终态，随时可以落库。
type SessionRecord struct {
	SessionID   string
	DeviceID    string
	Transport   Transport
	RemoteAddr  string
	StartedAt   time.Time
	LastActive  time.Time
	EndedAt     time.Time
	CloseReason string
	BytesIn     int64
	BytesOut    int64
	FramesIn    int64
	FramesOut   int64
}
