package devices

// VendorStatus 是网关库 vendor.status 列的厂商启用状态，用于厂商资料筛选与开放判断。
// 取值与 migrations/gateway_db/0001_init.sql 中该 ENUM 列一致（enabled/disabled）。
//
// 与 DeviceMetaStatus、GatewayDeviceStatus 分立：三者字面量部分相同，但分别属于
// 厂商表、中央设备表与网关设备表，不得互相赋值或复用判定方法。
type VendorStatus string

const (
	// VendorEnabled 表示厂商已启用，其下设备可被创建与接入。
	VendorEnabled VendorStatus = "enabled"
	// VendorDisabled 表示厂商已停用。
	VendorDisabled VendorStatus = "disabled"
)

// Known 判断是否属于该列的数据库合法集合（两值）。
func (s VendorStatus) Known() bool {
	return s == VendorEnabled || s == VendorDisabled
}

// Filtered 判断厂商列表筛选参数是否需要生效。
// 空串表示不筛选，是跨进程约定：调用方（integration/gateway 与后台查询）都以空串编码「不过滤」，
// 因此该判定独立于 Known()，不得据此把空串当作非法值拒绝。
func (s VendorStatus) Filtered() bool {
	return s != ""
}

// AllStatuses 是 VendorStatus 的全量取值串，供入参白名单校验直接引用。
const AllVendorStatuses = "enabled disabled"

// VendorProtocol 是网关库 vendor.protocol 列的厂商传输配置，决定网关以何种方式接洽厂商侧设备。
// 取值与 migrations/gateway_db/0001_init.sql 中该 ENUM 列一致（tcp/mqtt/hybrid）。
//
// 与 protocol.Transport（device_session.protocol 的两值词表）分立：
// 厂商配置决定「网关用什么方式接厂商」，会话传输记录「当前这条会话实际用了什么」，
// 二者不是同一层级，hybrid 不表示某条会话正在同时跑两种协议。
type VendorProtocol string

const (
	// VendorProtocolTCP 表示厂商侧仅提供 TCP 接入。
	VendorProtocolTCP VendorProtocol = "tcp"
	// VendorProtocolMQTT 表示厂商侧仅提供 MQTT 接入。
	VendorProtocolMQTT VendorProtocol = "mqtt"
	// VendorProtocolHybrid 表示厂商侧两种方式都提供，由网关按设备选择。
	VendorProtocolHybrid VendorProtocol = "hybrid"
)

// Known 判断是否属于该列的数据库合法集合（三值）。
func (p VendorProtocol) Known() bool {
	switch p {
	case VendorProtocolTCP, VendorProtocolMQTT, VendorProtocolHybrid:
		return true
	default:
		return false
	}
}

// AllVendorProtocols 是 VendorProtocol 的全量取值串，供入参白名单校验直接引用。
const AllVendorProtocols = "tcp mqtt hybrid"
