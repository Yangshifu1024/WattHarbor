package money

// ScanKind 是扫码解析结果的种类，由仓储按命中的查询分支派生，数据库中没有对应列。
// 取值只有 device 与 port：设备号解析出设备，端口码解析出具体端口。
//
// 与 PortStatus 相互独立：Kind 回答「扫到了什么」，PortStatus 回答「这个端口
// 现在处于什么状态」。二者之间不存在映射，任何一方都不能由另一方推出。
type ScanKind string

const (
	// ScanKindDevice 表示按设备编号解析，结果只有设备级信息。
	ScanKindDevice ScanKind = "device"
	// ScanKindPort 表示按端口二维码解析，结果含端口级信息。
	ScanKindPort ScanKind = "port"
)

// Valid 判断是否为已定义的取值。
func (k ScanKind) Valid() bool {
	switch k {
	case ScanKindDevice, ScanKindPort:
		return true
	default:
		return false
	}
}

// PortStatus 是 gateway_db 的 device_port.status 列的端口状态，用于扫码可用性
// 判定与启动占用。取值与 migrations/gateway_db/0001_init.sql 中该 ENUM 列一致。
//
// 该词表的权威在网关库，不是中央库；与 charge_process.port_status 无关——
// 后者是设备协议上报的原始状态码 tinyint，与本业务词表没有对应关系。
// full 与 fault 在当前 Go 代码中没有判定分支，保留以完整覆盖列约束。
type PortStatus string

const (
	// PortIdle 表示端口空闲，可被占用。
	PortIdle PortStatus = "idle"
	// PortCharging 表示端口正在充电。
	PortCharging PortStatus = "charging"
	// PortFull 表示端口已满，不可被占用。
	PortFull PortStatus = "full"
	// PortFault 表示端口故障。
	PortFault PortStatus = "fault"
	// PortDisabled 表示端口已停用。
	PortDisabled PortStatus = "disabled"
)

// Valid 判断是否为该列已定义的取值。
func (s PortStatus) Valid() bool {
	switch s {
	case PortIdle, PortCharging, PortFull, PortFault, PortDisabled:
		return true
	default:
		return false
	}
}
