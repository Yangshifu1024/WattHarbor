package dc589

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// Register 是设备注册请求 0xA0。
	Register byte = 0xA0
	// RegisterReply 是平台注册回复 0xA1。
	RegisterReply byte = 0xA1
	// RemoteControl 是平台远程控制命令 0xA2。
	RemoteControl byte = 0xA2
	// RemoteResult 是设备远程控制回复 0xA3。
	RemoteResult byte = 0xA3
	// Heartbeat 是设备心跳上报 0xA4。
	Heartbeat byte = 0xA4
	// HeartbeatReply 是平台心跳回复 0xA5。
	HeartbeatReply byte = 0xA5
	// HeartbeatInterval 设置心跳周期及端口遥测开关；自 5.8.6 起两者均由平台下发。
	HeartbeatInterval byte = 0xA6
	// HeartbeatSetReply 是设备心跳配置回复 0xA7。
	HeartbeatSetReply byte = 0xA7
	// TimeRequest 请求平台本地民用时间，TimeReply 返回校时应答。
	TimeRequest byte = 0xA8
	// TimeReply 是平台校时回复 0xA9。
	TimeReply byte = 0xA9
	// StartCharge 是平台启动命令 0xB7。
	StartCharge byte = 0xB7
	// StartReply 是设备启动回复 0xB8。
	StartReply byte = 0xB8
	// StopCharge 是平台停止命令 0xB9。
	StopCharge byte = 0xB9
	// StopReply 是设备停止回复 0xBA。
	StopReply byte = 0xBA
	// ChargeEnd 是设备充电结束上报 0xBB。
	ChargeEnd byte = 0xBB
	// ChargeEndReply 确认一次充电结束。
	ChargeEndReply byte = 0xBC
	// Fault 是设备故障上报 0xC0。
	Fault byte = 0xC0
	// FaultReply 是平台故障确认回复 0xC1。
	FaultReply byte = 0xC1
	// ChargingBand 是充电过程中主板可以主动发来的各端口上报，
	// 平台不必为此发任何请求。
	ChargingBand byte = 0xC2
)

// ErrPayload 表示命令类型、载荷长度或字段值无效。
var ErrPayload = errors.New("invalid 5.8.9 payload")

// Registration 保存 A0 注册帧的板号、固件版本、模块和 SIM 信息。
type Registration struct {
	BoardID         string
	HardwareVersion string
	SoftwareID      string
	SoftwareVersion uint16
	ModuleID        string
	SIM             string
	Signal          byte
}

// ParseRegistration 解码 45 字节 A0 注册载荷；板号必须是十进制 BCD。
func ParseRegistration(frame Frame) (Registration, error) {
	if frame.Command != Register || len(frame.Data) != 45 {
		return Registration{}, ErrPayload
	}
	board, err := decodeBCD(frame.Data[:8])
	if err != nil {
		return Registration{}, err
	}
	module := decodeIdentifier(frame.Data[26:34])
	sim := decodeIdentifier(frame.Data[34:44])
	return Registration{
		BoardID:         board,
		HardwareVersion: string(frame.Data[8:16]),
		SoftwareID:      string(frame.Data[16:24]),
		SoftwareVersion: binary.LittleEndian.Uint16(frame.Data[24:26]),
		ModuleID:        module,
		SIM:             sim,
		Signal:          frame.Data[44],
	}, nil
}

// 模块及 SIM 标识按原始半字节解码为大写十六进制，兼容厂商样例中的非十进制值。
func decodeIdentifier(data []byte) string {
	if result, err := decodeBCD(data); err == nil {
		return result
	}
	return strings.ToUpper(hex.EncodeToString(data))
}

// ChargeMode 决定启动额度按时间还是电量解释。
type ChargeMode byte

const (
	// ByTime 使用分钟作为启动额度。
	ByTime ChargeMode = 0
	// ByEnergy 使用 Wh 作为启动额度。
	ByEnergy ChargeMode = 1
	// PlatformBilling 使用分钟限定充电时间，费用由平台计算。
	PlatformBilling ChargeMode = 4
	// 拒绝硬件未实现的模式 2、3，以及不用于常规运营的长时模式 10、11、12。
	LongTime ChargeMode = 10
	// LongEnergy 是普通启动入口禁止使用的长时电量模式。
	LongEnergy ChargeMode = 11
	// LongPlatformBilling 是普通启动入口禁止使用的长时平台计费模式。
	LongPlatformBilling ChargeMode = 12
)

// NormalChargeModes 列出普通启动允许使用的时间、电量和平台计费模式。
var NormalChargeModes = map[ChargeMode]bool{
	ByTime: true, ByEnergy: true, PlatformBilling: true,
}

// IsNormal 判断模式是否可用于普通充电。
// 排障长时模式绕过时间和电量上限，必须排除。
func (m ChargeMode) IsNormal() bool { return NormalChargeModes[m] }

// StartCommand 保存 B7 启动所需的端口、订单、额度和卡信息。
type StartCommand struct {
	ConsumerType     uint8
	CardNumber       uint32
	CardBalanceUnits uint16
	Session          [6]byte
	Port             byte
	OrderBCD         [8]byte
	Mode             ChargeMode
	Quantity         uint16 // 依 Mode 而定为分钟或 0.001 kWh
}

// BuildStart 编码扫码类型 2 或在线卡类型 3 的启动命令，卡模式包含卡号和余额。
// 调用方必须先完成支付授权；长时变体不允许通过普通启动入口。
func BuildStart(command StartCommand) (Frame, error) {
	if command.Port == 0 || !command.Mode.IsNormal() || command.Quantity == 0 {
		return Frame{}, ErrPayload
	}
	data := make([]byte, 19)
	data[0] = command.Port
	copy(data[1:9], command.OrderBCD[:])
	data[9] = byte(command.Mode)
	data[10] = 2
	if command.ConsumerType != 0 && command.ConsumerType != 2 && command.ConsumerType != 3 {
		return Frame{}, ErrPayload
	}
	if command.ConsumerType == 3 {
		if command.CardNumber == 0 {
			return Frame{}, ErrPayload
		}
		data[10] = 3
		binary.LittleEndian.PutUint32(data[13:17], command.CardNumber)
		binary.LittleEndian.PutUint16(data[17:19], command.CardBalanceUnits)
	} else if command.CardNumber != 0 || command.CardBalanceUnits != 0 {
		return Frame{}, ErrPayload
	}
	binary.LittleEndian.PutUint16(data[11:13], command.Quantity)
	return Frame{Command: StartCharge, Session: command.Session, Data: data}, nil
}

// BuildStop 编码 B9 停止命令；端口号必须大于零。
func BuildStop(session [6]byte, port byte) (Frame, error) {
	if port == 0 {
		return Frame{}, ErrPayload
	}
	return Frame{Command: StopCharge, Session: session, Data: []byte{port}}, nil
}

// BuildReboot 编码 A2 主板重启命令，其余控制字段填零。
func BuildReboot(session [6]byte) Frame {
	data := make([]byte, 10)
	data[0] = 1
	return Frame{Command: RemoteControl, Session: session, Data: data}
}

// CommandResult 保存启动或停止回复中的结果码和端口号。
type CommandResult struct {
	Code byte
	Port byte
}

// ParseCommandResult 解码 B8 或 BA 的两字节回复，保留设备结果码供调用方判断。
func ParseCommandResult(frame Frame) (CommandResult, error) {
	if frame.Command != StartReply && frame.Command != StopReply || len(frame.Data) != 2 {
		return CommandResult{}, ErrPayload
	}
	return CommandResult{Code: frame.Data[0], Port: frame.Data[1]}, nil
}

// BuildRegisterReply 编码 A1 注册成功回复，附带分配的会话号和 UTC+8 时间。
func BuildRegisterReply(session [6]byte, now time.Time) Frame {
	data := make([]byte, 7)
	data[0] = 0 // 已连接
	// 注册与校时响应使用相同的协议民用时区编码，避免宿主机时区造成时间偏移。
	encodeTime(data[1:], now.In(chinaLocation))
	return Frame{Command: RegisterReply, Session: session, Data: data}
}

// BuildHeartbeatReply 编码 A5 心跳确认回复。
func BuildHeartbeatReply(session [6]byte) Frame {
	return Frame{Command: HeartbeatReply, Session: session, Data: []byte{1}}
}

// BuildTimeReply 编码 A9，将平台时间转换为 UTC+8 后写入六字节 BCD。
func BuildTimeReply(session [6]byte, now time.Time) Frame {
	data := make([]byte, 6)
	encodeTime(data, now.In(chinaLocation))
	return Frame{Command: 0xA9, Session: session, Data: data}
}

// BuildHeartbeatInterval 编码 0xA6 心跳周期与端口遥测开关；周期必须大于零。
func BuildHeartbeatInterval(session [6]byte, seconds uint16, portStatus bool) (Frame, error) {
	if seconds == 0 {
		return Frame{}, ErrPayload
	}
	flag := byte(0)
	if portStatus {
		flag = 1
	}
	return Frame{Command: HeartbeatInterval, Session: session,
		Data: []byte{byte(seconds), byte(seconds >> 8), flag}}, nil
}

// ParseHeartbeatSetReply 读取 0xA7 确认帧。
func ParseHeartbeatSetReply(frame Frame) (bool, error) {
	if frame.Command != HeartbeatSetReply || len(frame.Data) != 1 {
		return false, ErrPayload
	}
	return frame.Data[0] == 0, nil
}

// BuildChargeEndReply 编码 BC，确认已收到指定端口的充电结束上报。
func BuildChargeEndReply(session [6]byte, port byte) Frame {
	return Frame{Command: ChargeEndReply, Session: session, Data: []byte{0, port}}
}

// BuildFaultReply 编码 C1，确认已收到指定端口的故障上报。
func BuildFaultReply(session [6]byte, port byte) Frame {
	return Frame{Command: FaultReply, Session: session, Data: []byte{0, port}}
}

func encodeTime(dst []byte, value time.Time) {
	values := [6]int{value.Year() % 100, int(value.Month()), value.Day(), value.Hour(), value.Minute(), value.Second()}
	for i, n := range values {
		dst[i] = byte(n/10<<4 | n%10)
	}
}

func decodeBCD(data []byte) (string, error) {
	out := make([]byte, len(data)*2)
	for i, b := range data {
		hi, lo := b>>4, b&0x0f
		if hi > 9 || lo > 9 {
			return "", fmt.Errorf("%w: non-decimal BCD", ErrPayload)
		}
		out[2*i], out[2*i+1] = '0'+hi, '0'+lo
	}
	return string(out), nil
}

// HeartbeatSetting 表示 0xA6 请求设置的心跳周期和端口遥测开关。
type HeartbeatSetting struct {
	Seconds    uint16
	PortStatus bool
}

// BuildHeartbeatSetReply 编码 0xA7；周期为零时使用拒绝码。
func BuildHeartbeatSetReply(setting HeartbeatSetting) Frame {
	code := byte(0)
	if setting.Seconds == 0 {
		code = 1
	}
	return Frame{Command: HeartbeatSetReply, Data: []byte{code}}
}

// ParseHeartbeatInterval 解码 0xA6，返回周期和端口遥测开关。
func ParseHeartbeatInterval(frame Frame) (HeartbeatSetting, error) {
	if frame.Command != HeartbeatInterval || len(frame.Data) != 3 {
		return HeartbeatSetting{}, ErrPayload
	}
	return HeartbeatSetting{
		Seconds:    binary.LittleEndian.Uint16(frame.Data[0:2]),
		PortStatus: frame.Data[2] == 1,
	}, nil
}
