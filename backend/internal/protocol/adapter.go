// Package protocol 定义设备事件、命令和连接接口，让充电逻辑独立于厂商协议。
package protocol

import (
	"context"
	"net"
	"time"
)

// Registration 保存设备注册时的协议、编号和固件版本。
type Registration struct {
	Protocol        string
	DeviceID        string
	HardwareVersion string
	SoftwareVersion string
	ReceivedAt      time.Time
}

// EventType 标识已解码设备事件的类别。
type EventType string

const (
	// Heartbeat 表示设备状态和充电读数上报。
	Heartbeat EventType = "heartbeat"
	// StartResult 表示设备对启动命令的回复。
	StartResult EventType = "start_result"
	// StopResult 表示设备对停止命令的回复。
	StopResult EventType = "stop_result"
	// ChargeEnd 表示设备主动上报充电结束。
	ChargeEnd EventType = "charge_end"
	// Fault 表示设备故障上报。
	Fault EventType = "fault"
	// RemoteResult 表示设备对远程控制的回复。
	RemoteResult EventType = "remote_result"
	// ConfigResult 表示心跳、参数表及电源控制的设备响应，包含拒绝原因，供运维确认配置是否生效。
	ConfigResult EventType = "config_result"
	// Telemetry 表示单独上报的遥测数据。
	Telemetry EventType = "telemetry"
	// TimeSync 表示设备校时请求或回复。
	TimeSync EventType = "time_sync"
	// CardSwipe 表示一次设备刷卡上报。
	CardSwipe EventType = "card_swipe"
	// CardBalanceQuery 表示设备查询卡余额。
	CardBalanceQuery EventType = "card_balance_query"
)

// Event 保存设备事件的解码结果及原始载荷，供存储和业务处理使用。
type Event struct {
	EventID    string
	CardNumber uint32
	Protocol   string
	DeviceID   string
	Type       EventType
	Port       uint8
	ResultCode uint8
	FaultCode  uint8
	// EnergyMilliKWh 是结束事件的累计电量，实际单位为 Wh。
	// 适配器将 mWh 除以 1000；入库时再将 Wh 除以 1000，转换为 kWh。
	// 字段名也是历史事件 JSON 的键，不能直接改名，否则旧记录的电量会读成零。
	EnergyMilliKWh uint32
	PowerDeciWatts uint32
	StopReason     uint8
	ConsumerType   uint8
	ReceivedAt     time.Time
	RawPayload     []byte
	SessionID      [6]byte
	// 电表与安全相关字段由厂商适配器在事件被持久化之前解出来。
	// RawPayload 仍然保留，供审计与重放使用。
	OrderNumber    string
	ChargedSeconds uint32
	StartedAt      time.Time
	EndedAt        time.Time
	VoltageV       uint16
	TemperatureC   int16
	DeviceStatus   uint8
	Signal         uint8
	PortStates     []uint8
	ChargingPorts  []PortTelemetry
}

// PortTelemetry 保存单端口充电读数：时长单位秒，电量单位 mWh，功率单位 0.1 W。
type PortTelemetry struct {
	Port           uint8
	RemainingSecs  uint32
	ChargedSeconds uint32
	RemainingMWh   uint32
	ChargedMWh     uint32
	PowerDeciWatts uint32
}

// Sink 负责设备鉴权和事件持久化；保存成功后适配器才能向设备确认帧。
type Sink interface {
	Register(context.Context, Registration) error
	Record(context.Context, Event) error
}

// SessionRecorder 在连接结束后持久化时间、地址及流量统计。
// 与 Sink 独立，未实现会话存储的适配器仍可处理设备事件。
type SessionRecorder interface {
	RecordSession(context.Context, SessionRecord) error
}

// CloseReason 表示持久化的连接结束原因，使用稳定标记供审计分组。
type CloseReason string

const (
	// CloseDeviceClosed 表示设备关闭连接。
	CloseDeviceClosed CloseReason = "device_closed"
	// CloseReadTimeout 表示读取设备帧超时。
	CloseReadTimeout CloseReason = "read_timeout"
	// CloseReadError 表示读取设备帧失败。
	CloseReadError CloseReason = "read_error"
	// CloseProtocol 表示设备帧或注册顺序无效。
	CloseProtocol CloseReason = "protocol_error"
	// CloseRejected 表示设备注册被拒绝。
	CloseRejected CloseReason = "rejected"
	// CloseReplaced 表示连接被同设备的新连接替换。
	CloseReplaced CloseReason = "replaced"
	// CloseContextEnded 表示服务取消了连接处理。
	CloseContextEnded CloseReason = "context_ended"
	// CloseAbandoned 表示写入会话记录时连接已不可达，由网关侧补记。
	CloseAbandoned CloseReason = "abandoned"
)

// Adapter 处理一种厂商协议的设备连接。
type Adapter interface {
	Name() string
	ServeConn(context.Context, net.Conn, Sink) error
}
