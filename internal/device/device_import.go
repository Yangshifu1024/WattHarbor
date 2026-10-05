package device

// 设备批量导入的记录类型。任务表 device_import 的落库投影与请求体 DTO 一并
// 定义在所属用例：ImportService 管理事务及外部能力，HTTP 仅适配请求和响应。

// ImportDevice 描述批量导入的设备身份、厂商、站点、端口数、型号及计费模式。
// 设备计量能力由所选协议确定。
type ImportDevice struct {
	ProtocolAdapter string  `json:"protocol_adapter"` // 服务根据原厂商公开事实冻结的协议标签，覆盖客户端声明
	DeviceID        string  `json:"device_id"`        // 设备编号，全局唯一，8–32 位字母数字及 _ -，大小写不敏感判重
	VendorID        uint64  `json:"vendor_id"`        // 厂商 ID，必须非 0
	StationID       uint64  `json:"station_id"`       // 所属站点 ID，必须非 0 且该站点处于 active
	PortCount       uint8   `json:"port_count"`       // 充电端口数，必须非 0
	Model           *string `json:"model"`            // 型号，可空；非空时最多 128 字
	// 兼容导入字段；实际计量能力按协议计算，不依赖客户端人工声明。
	ChargeMode            string `json:"charge_mode"`             // 计费方式，取值为计费引擎认可的六种模式之一；留空表示不覆盖
	ReportsEnergy         bool   `json:"reports_energy"`          // 原选定协议的电量能力，客户端声明会被覆盖，不持久复制到设备表
	ReportsSegmentedPower bool   `json:"reports_segmented_power"` // 原选定协议的分段功率能力，客户端声明会被覆盖
}

// ImportJob 是一次批量导入任务在 device_import 表中的状态投影，也是导入列表接口的返回行。
// 原始请求体只存库不外发（json："-"），列表页因此看不到设备明细，只能看任务进展。
type ImportJob struct {
	ImportID    string  `json:"import_id"`  // 导入任务号，客户端生成的幂等键
	Status      string  `json:"status"`     // 任务状态：pending 待执行 / completed 已完成 / failed 失败可重试
	LastError   *string `json:"last_error"` // 最近一次失败原因，成功时为 nil
	RequestJSON string  `json:"-"`          // 原始设备清单 JSON，重试时据此重放，不返回给前端
	ActorID     uint64  `json:"actor_id"`   // 发起导入的管理员 ID
	Attempts    uint32  `json:"attempts"`   // 已执行次数，每次调用网关后 +1
}
