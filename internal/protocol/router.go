package protocol

import "context"

// SessionRouter 是适配器向设备接入运行时登记会话所需的最小能力。
//
// 适配器不持有 Registry 的具体类型，只声明依赖这个形状：注册一条设备到
// 会话的映射，并取得解绑函数。这样会话索引的实现可以随运行时一起下沉到
// gateway，而不必让契约层反向依赖运行时层。
//
// Send 与 Connected 刻意不在此接口内：命令下发是运行时的职责，适配器不参与，
// 把它们加进来只会把下发路径泄漏成跨进程契约。
type SessionRouter interface {
	Attach(deviceID string, session Session, onReplaced func(Session)) func()
}

// CommandSender 由运行时实现，负责把命令发到设备当前连接。
// 适配器不需要它，此处声明是为了让 gateway 侧的实现可以同时满足两个接口。
type CommandSender interface {
	Send(ctx context.Context, deviceID string, command Command) error
}
