package device

import (
	"chargepilot/internal/domain/charging"
	"chargepilot/internal/protocol"
)

// 批量导入请求和任务记录定义在 device 用例内；ImportService 管理事务和网关调用，HTTP 负责格式转换。

// ImportDevice 保存设备编号、厂商、站点、端口数、型号和计费模式；计量能力由所选协议确定。
type ImportDevice struct {
	ProtocolAdapter protocol.AdapterID `json:"protocol_adapter"` // 导入时按网关厂商资料确定的协议标签，覆盖客户端声明
	DeviceID        string             `json:"device_id"`        // 设备编号，全局唯一，8–32 位字母数字及 _ -，大小写不敏感判重
	VendorID        uint64             `json:"vendor_id"`        // 厂商 ID，必须非 0
	StationID       uint64             `json:"station_id"`       // 所属站点 ID，必须非 0 且该站点处于 active
	PortCount       uint8              `json:"port_count"`       // 充电端口数，必须非 0
	Model           *string            `json:"model"`            // 型号，可空；非空时最多 128 字
	// 兼容导入字段；实际计量能力按协议计算，不依赖客户端人工声明。
	ChargeMode            charging.ChargeMode `json:"charge_mode"`             // 设备资料中的计费模式，词表见 domain/charging；新建时留空使用数据库默认值，已有设备不修改
	ReportsEnergy         bool                `json:"reports_energy"`          // 选定协议的电量能力，客户端声明会被覆盖，不重复保存到设备表
	ReportsSegmentedPower bool                `json:"reports_segmented_power"` // 选定协议的分段功率能力，客户端声明会被覆盖
}

// ImportJob 是导入任务状态，也是列表返回记录；请求清单只存数据库，不返回设备明细。
type ImportJob struct {
	ImportID string `json:"import_id"` // 导入任务号，客户端生成的幂等键
	// device_import.status 三值：pending／completed／failed。
	Status      ImportStatus `json:"status"`     // 任务状态：pending 待执行 / completed 已完成 / failed 失败可重试
	LastError   *string      `json:"last_error"` // 最近一次失败因，成功时为 nil
	RequestJSON string       `json:"-"`          // 补齐服务端协议信息后的设备清单 JSON，重试使用，不返回给前端
	ActorID     uint64       `json:"actor_id"`   // 发起导入的管理员 ID
	Attempts    uint32       `json:"attempts"`   // 已保存的开通尝试次数；网关失败或完成状态提交时加一
}
