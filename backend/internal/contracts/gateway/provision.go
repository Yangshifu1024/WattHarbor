package gateway

import "time"

const (
	ProvisionPath = "/api/v1/internal/devices/provision" // 设备开通原内部端点
	VendorPath    = "/api/v1/internal/vendors"           // 厂商内部接口，查询详情时追加十进制 ID
)

// ProvisionDevice 定义要开通的设备编号和端口配置，不包含中央计费和运营配置。
type ProvisionDevice struct {
	DeviceID  string  `json:"device_id"`  // 设备全局编号，用于协议寻址
	VendorID  uint64  `json:"vendor_id"`  // gateway 中的厂商 ID
	StationID uint64  `json:"station_id"` // 中央站点 ID，仅标识归属
	PortCount uint8   `json:"port_count"` // 端口数，DC589 最多二十路
	Model     *string `json:"model"`      // 设备型号，nil 表示未指定
}

// ProvisionRequest 批量开通设备，每批最多 100 台。
type ProvisionRequest struct {
	Devices []ProvisionDevice `json:"devices"` /* 同批设备编号 */
}

// ProvisionReply 表示设备和端口配置已保存，不代表实际设备已上线。
type ProvisionReply struct {
	Provisioned int `json:"provisioned"` /* 已确认批次设备数，包含重复提交的设备 */
}

// VendorReply 是对外公开厂商字典；适配器私有配置和密钥仍留在 gateway。
type VendorReply struct {
	ID           uint64     `json:"id"`            // 原厂商主键
	VendorCode   string     `json:"vendor_code"`   // 创建后不可变的厂商编码
	VendorName   string     `json:"vendor_name"`   // 当前可见名称
	AdapterClass string     `json:"adapter_class"` // 适配器名称，用于检查设备支持的功能
	Protocol     string     `json:"protocol"`      // tcp／mqtt／hybrid，运行设备开通目前要求 tcp
	Status       string     `json:"status"`        // enabled／disabled
	EnabledAt    *time.Time `json:"enabled_at"`    // 最近启用时刻，未启用可空
	CreatedAt    time.Time  `json:"created_at"`    // 创建时间，UTC
	UpdatedAt    time.Time  `json:"updated_at"`    // 最近修改时间，UTC
}

// VendorFailure 保留厂商服务已有可公开错误契约，不含网络地址、响应正文或服务令牌。
type VendorFailure struct {
	Status  int    // 可公开的 HTTP 400／404／409；依赖失败使用 503
	Code    int    // 业务码，依赖失败为 5003
	Message string // 已经由服务确定的公开提示
}

// Error 返回可向调用方公开的错误提示。
func (e *VendorFailure) Error() string { return e.Message }

// VendorRequest 是完整厂商公开写入字段；服务端检查格式、禁止修改编码，并检查是否允许更换协议。
type VendorRequest struct {
	VendorCode   string `json:"vendor_code"`   // 厂商编码，创建后不可改
	VendorName   string `json:"vendor_name"`   // 可见名称，服务端去空白并校验
	AdapterClass string `json:"adapter_class"` // 适配器标签
	Protocol     string `json:"protocol"`      // tcp／mqtt／hybrid，网关检查是否支持该协议
	Status       string `json:"status"`        // enabled／disabled
}

// VendorListRequest 是厂商列表的具名查询，不允许消费者拼接路径或任意查询参数。
type VendorListRequest struct {
	Page     int      // 起始页码，从 1 开始
	PageSize int      // 每页条数，1 至 100
	Keyword  string   // 搜索关键词；客户端仅 URL 编码，SQL LIKE 转义由 gateway 仓储处理
	Status   string   // 空、enabled 或 disabled
	IDs      []uint64 // 当前账号的厂商范围，空表示不限定厂商
}

// VendorListReply 是网关公开列表响应，不接受上游授予中央账号权限。
type VendorListReply struct {
	Items    []VendorReply `json:"items"`     // 当页公开字典，可为空
	Total    int64         `json:"total"`     // 当前筛选的总条数
	Page     int           `json:"page"`      // 响应页码
	PageSize int           `json:"page_size"` // 响应每页条数
}
