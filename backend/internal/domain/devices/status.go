package devices

// DeviceMetaStatus 是中央库 device_meta.status 列的运营状态，用于设备启停与扫码可用性判定。
// 取值与 migrations/central_db/0001_init.sql 中该 ENUM 列一致（enabled/disabled/retired/fault）。
//
// 与 GatewayDeviceStatus 分立：后者是网关库 device.status 的三值词表，没有 fault。
// 两者的 enabled/disabled/retired 字面量相同但语义与权威列不同，不得互相赋值或比较。
type DeviceMetaStatus string

const (
	// DeviceEnabled 表示设备已启用，可被分配与启动。
	DeviceEnabled DeviceMetaStatus = "enabled"
	// DeviceDisabled 表示设备已停用，仍保留资料与端口记录。
	DeviceDisabled DeviceMetaStatus = "disabled"
	// DeviceRetired 表示设备已退役，不再参与运营分配。
	DeviceRetired DeviceMetaStatus = "retired"
	// DeviceFault 表示设备处于故障状态；当前无写入路径，保留以完整覆盖列约束。
	DeviceFault DeviceMetaStatus = "fault"
)

// Known 判断是否属于该列的数据库合法集合（四值）。
// 供读取路径判断是否需要降级处理；读取路径遇到未知值时保持原值透传，不得据此拒绝或替换。
func (s DeviceMetaStatus) Known() bool {
	switch s {
	case DeviceEnabled, DeviceDisabled, DeviceRetired, DeviceFault:
		return true
	default:
		return false
	}
}

// ManageWritable 判断是否属于管理端设备启停操作允许写入的集合（两值）。
// 该集合是 PUT /api/v1/admin/devices/:id/status 这一个操作的准入规则，不是数据库合法集合：
// 仓储函数 SaveDeviceStatus 本身不做取值限制，不能由本方法推断所有写入路径都不产生 fault。
func (s DeviceMetaStatus) ManageWritable() bool {
	return s == DeviceEnabled || s == DeviceDisabled
}

// GatewayDeviceStatus 是网关库 device.status 列的设备状态，用于设备开通与运行管理。
// 取值与 migrations/gateway_db/0001_init.sql 中该 ENUM 列一致（enabled/disabled/retired）。
//
// 与 DeviceMetaStatus 分立：网关库该列没有 fault，四个枚举中只有中央库有。
// 判断字段合法性必须先确认数据源，不得用网关列推断中央字段，反之亦然。
type GatewayDeviceStatus string

const (
	// GatewayDeviceEnabled 表示网关侧设备在线可用。
	GatewayDeviceEnabled GatewayDeviceStatus = "enabled"
	// GatewayDeviceDisabled 表示网关侧设备已停用。
	GatewayDeviceDisabled GatewayDeviceStatus = "disabled"
	// GatewayDeviceRetired 表示网关侧设备已退役。
	GatewayDeviceRetired GatewayDeviceStatus = "retired"
)

// Known 判断是否属于该列的数据库合法集合（三值）。
func (s GatewayDeviceStatus) Known() bool {
	switch s {
	case GatewayDeviceEnabled, GatewayDeviceDisabled, GatewayDeviceRetired:
		return true
	default:
		return false
	}
}

// AllDeviceMetaStatuses 是 DeviceMetaStatus 的全量取值串，供读取筛选白名单直接引用。
// 顺序与上方常量声明一致。新增状态取值不会自动进入该白名单，需要按业务准入规则显式补充。
const AllDeviceMetaStatuses = "enabled disabled retired fault"
