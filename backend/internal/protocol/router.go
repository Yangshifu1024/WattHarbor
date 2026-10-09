package protocol

import "context"

// SessionRouter 让适配器登记设备连接，并取得解绑函数。
// 该接口只管理会话映射，命令发送和在线查询由运行时负责。
type SessionRouter interface {
	Attach(deviceID string, session Session, onReplaced func(Session)) func()
}

// CommandSender 由运行时实现，负责向设备当前连接发送命令。
type CommandSender interface {
	Send(ctx context.Context, deviceID string, command Command) error
}
