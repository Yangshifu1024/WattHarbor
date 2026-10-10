package devices

// ChargeCommandStatus 是网关库 charge_command.status 列的启动命令状态，
// 用于命令落库、设备回执推进与跨进程回执传递。
// 取值与 migrations/gateway_db/0001_init.sql 中该 ENUM 列一致。
//
// 与 StopCommandStatus 分立：停机命令词表没有 stopping。若两者取并集，
// 会让停机命令持有永不出现的取值，也会让启动路径的 stopping 判定扩散到停机路径。
type ChargeCommandStatus string

const (
	// CommandPending 表示命令已入库、尚未下发到设备。
	CommandPending ChargeCommandStatus = "pending"
	// CommandSent 表示命令已下发，等待设备回执。
	CommandSent ChargeCommandStatus = "sent"
	// CommandAcked 表示设备已确认，命令生效。
	CommandAcked ChargeCommandStatus = "acked"
	// CommandStopping 表示启动过程中转入停机流程，该状态只属于启动命令。
	CommandStopping ChargeCommandStatus = "stopping"
	// CommandRejected 表示设备明确拒绝或下发失败。
	CommandRejected ChargeCommandStatus = "rejected"
)

// Known 判断是否属于该列的数据库合法集合（五值）。
func (s ChargeCommandStatus) Known() bool {
	switch s {
	case CommandPending, CommandSent, CommandAcked, CommandStopping, CommandRejected:
		return true
	default:
		return false
	}
}

// StopCommandStatus 是网关库 charge_stop_command.status 列的停机命令状态，
// 用于停机命令落库与回执推进。取值与 migrations/gateway_db/0001_init.sql 中该 ENUM 列一致。
//
// 与 ChargeCommandStatus 分立：该列没有 stopping，取值不得与启动命令取并集。
type StopCommandStatus string

const (
	// StopCommandPending 表示停机命令已入库、尚未下发到设备。
	StopCommandPending StopCommandStatus = "pending"
	// StopCommandSent 表示停机命令已下发，等待设备回执。
	StopCommandSent StopCommandStatus = "sent"
	// StopCommandAcked 表示设备已确认停机。
	StopCommandAcked StopCommandStatus = "acked"
	// StopCommandRejected 表示设备明确拒绝停机或下发失败。
	StopCommandRejected StopCommandStatus = "rejected"
)

// Known 判断是否属于该列的数据库合法集合（四值）。
func (s StopCommandStatus) Known() bool {
	switch s {
	case StopCommandPending, StopCommandSent, StopCommandAcked, StopCommandRejected:
		return true
	default:
		return false
	}
}
