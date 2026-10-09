package protocol

import "context"

// CommandKind 标识与厂商协议无关的命令类型。
// worker 返回的 JSON 和 gateway 的处理逻辑必须使用相同取值；
// 新增卡回复类型时，同步修改 worker/charge/cards.go 和 gateway/control/cards.go。
type CommandKind string

const (
	// CommandCardDenied 向设备发送刷卡拒绝回复。
	CommandCardDenied CommandKind = "card_denied"
	// CommandCardBalance 向设备发送卡余额回复。
	CommandCardBalance CommandKind = "card_balance"
)

// CommandStart、CommandStop 和 CommandReboot 仅由 gateway 使用，不经 worker 传递。
const (
	CommandStart  CommandKind = "start"
	CommandStop   CommandKind = "stop"
	CommandReboot CommandKind = "reboot"
)

// Command 表示设备命令；业务用例先鉴权并保存，再由适配器编码为厂商协议帧。
type Command struct {
	ConsumerType     uint8
	CardNumber       uint32
	CardBalanceUnits uint16
	CardInvalid      bool
	Kind             CommandKind
	SessionID        [6]byte
	Port             uint8
	OrderBCD         [8]byte
	Mode             uint8
	Quantity         uint16
}

// Session 是适配器交给运行时的连接句柄：运行时用它下发命令或断开连接。
// 具体实现由各协议适配器提供。
type Session interface {
	Send(context.Context, Command) error
	Close() error
}
