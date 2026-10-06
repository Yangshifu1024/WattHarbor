package protocol

import "context"

// CommandKind 标识一条与厂商协议无关的命令。
//
// 它是 worker 与 gateway 之间的 wire contract：worker 的 JSON 响应里带这个
// 字符串，gateway 据此分派处理，两端的取值必须一致。新增取值时
// worker/charge/cards.go 与 gateway/control/cards.go 要同步。
type CommandKind string

const (
	CommandCardDenied  CommandKind = "card_denied"
	CommandCardBalance CommandKind = "card_balance"
)

// CommandStart / CommandStop / CommandReboot 供 gateway 运行时内部使用，
// 不经 worker 传递，故不与上面两个 wire 取值放在一起。
const (
	CommandStart  CommandKind = "start"
	CommandStop   CommandKind = "stop"
	CommandReboot CommandKind = "reboot"
)

// Command 表示与厂商协议无关的命令，须由领域层持久化并鉴权后发送。
// 每个字段都会被 dc589 翻译成该协议的帧，故本结构不含任何协议细节。
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
