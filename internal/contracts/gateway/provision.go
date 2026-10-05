package gateway

import "time"

const (
	ProvisionPath = "/api/v1/internal/devices/provision" // 设备开通原内部端点
	VendorPath    = "/api/v1/internal/vendors"           // 厂商字典原内部端点，详情追加十进制 ID
)

// ProvisionDevice 是 gateway 开通设备时冻结的原身份，不含中央运营计费配置。
type ProvisionDevice struct {
	DeviceID  string  `json:"device_id"`  // 原设备全局号，协议寻址身份
	VendorID  uint64  `json:"vendor_id"`  // 原 gateway 厂商主键
	StationID uint64  `json:"station_id"` // 原中央站点主键，仅作为归属
	PortCount uint8   `json:"port_count"` // 原端口数，DC589 最多二十路
	Model     *string `json:"model"`      // 原设备型号，nil 保持未声明
}

// ProvisionRequest 是最多一百台设备的原同步开通请求。
type ProvisionRequest struct {
	Devices []ProvisionDevice `json:"devices"` /* 同批原设备身份 */
}

// ProvisionReply 表示 gateway 已提交该批开通身份与端口，未确认物理设备上线。
type ProvisionReply struct {
	Provisioned int `json:"provisioned"` /* 已确认批次设备数，含原请求重放 */
}

// VendorReply 是对外公开厂商字典；适配器私有配置和密钥仍留在 gateway。
type VendorReply struct {
	ID           uint64     `json:"id"`            // 原厂商主键
	VendorCode   string     `json:"vendor_code"`   // 创建后不可变的厂商编码
	VendorName   string     `json:"vendor_name"`   // 当前可见名称
	AdapterClass string     `json:"adapter_class"` // 选定适配器标签，用于准入能力判断
	Protocol     string     `json:"protocol"`      // tcp／mqtt／hybrid，运行设备开通目前要求 tcp
	Status       string     `json:"status"`        // enabled／disabled
	EnabledAt    *time.Time `json:"enabled_at"`    // 最近启用时刻，未启用可空
	CreatedAt    time.Time  `json:"created_at"`    // 原创建 UTC 时刻
	UpdatedAt    time.Time  `json:"updated_at"`    // 原最近修改 UTC 时刻
}

// VendorFailure 保留厂商服务已有可公开错误契约，不含网络地址、响应正文或服务令牌。
type VendorFailure struct {
	Status  int    // 原可公开 HTTP 400／404／409；依赖失败使用 503
	Code    int    // 原业务码，依赖失败为 5003
	Message string // 已经由服务确定的公开提示
}

// Error 返回可公开提示，无 IO 或状态变更。
func (e *VendorFailure) Error() string { return e.Message }

// VendorRequest 是完整厂商公开写入字段；服务端负责格式、编码不可变与协议变更准入。
type VendorRequest struct {
	VendorCode   string `json:"vendor_code"`   // 原厂商编码，创建后不可改
	VendorName   string `json:"vendor_name"`   // 可见名称，服务端去空白并校验
	AdapterClass string `json:"adapter_class"` // 适配器标签
	Protocol     string `json:"protocol"`      // tcp／mqtt／hybrid，实际准入由网关决定
	Status       string `json:"status"`        // enabled／disabled
}

// VendorListRequest 是厂商列表的具名查询，不允许消费者拼接路径或任意查询参数。
type VendorListRequest struct {
	Page     int      // 起始页码，一开始
	PageSize int      // 每页条数，一至一百
	Keyword  string   // 原搜索关键词，由客户端转义
	Status   string   // 空、enabled 或 disabled
	IDs      []uint64 // 当前账号的厂商范围，空表示不限定厂商
}

// VendorListReply 是网关公开列表响应，不接受上游授予中央账号权限。
type VendorListReply struct {
	Items    []VendorReply `json:"items"`     // 当页公开字典，可为空
	Total    int64         `json:"total"`     // 当前筛选的总条数
	Page     int           `json:"page"`      // 原响应页码
	PageSize int           `json:"page_size"` // 原响应每页条数
}
