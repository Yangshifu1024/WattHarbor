package protocol

import "time"

// Transport 记录 tcp 或 mqtt，与 device_session.protocol 枚举一致。
// 该字段表示传输方式，不填写 dc589 等设备协议名称。
type Transport string

const (
	// TransportTCP 表示 TCP 传输。
	TransportTCP Transport = "tcp"
	// TransportMQTT 表示 MQTT 传输。
	TransportMQTT Transport = "mqtt"
)

// Known 判断是否属于 device_session.protocol 列的数据库合法集合（两值）。
// 仅用于识别未知值；读取路径遇到未知值时保持原值透传，不得据此拒绝或替换。
func (t Transport) Known() bool {
	return t == TransportTCP || t == TransportMQTT
}

// SessionRecord 保存连接结束时的审计记录。
// CloseReason 对应 device_session.close_reason 列，该列是 VARCHAR 开放集合，
// 不提供统一校验；新增取值不得反向拒绝已写入的历史记录。
type SessionRecord struct {
	SessionID   string
	DeviceID    string
	Transport   Transport
	RemoteAddr  string
	StartedAt   time.Time
	LastActive  time.Time
	EndedAt     time.Time
	CloseReason CloseReason
	BytesIn     int64
	BytesOut    int64
	FramesIn    int64
	FramesOut   int64
}
