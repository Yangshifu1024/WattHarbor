package gateway

import (
	"time"
	"wattharbor/internal/domain/devices"
	"wattharbor/internal/protocol"
)

const (
	ProvisionPath = "/api/v1/internal/devices/provision" // 保存设备和端口配置的内部接口
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

// VendorReply 返回厂商的公开配置，省略适配器私有配置和密钥。
type VendorReply struct {
	ID           uint64                 `json:"id"`            // 厂商 ID
	VendorCode   string                 `json:"vendor_code"`   // 创建后不可变的厂商编码
	VendorName   string                 `json:"vendor_name"`   // 当前可见名称
	AdapterClass protocol.AdapterID     `json:"adapter_class"` // 适配器名称，用于检查设备支持的功能
	Protocol     devices.VendorProtocol `json:"protocol"`      // tcp／mqtt／hybrid，运行设备开通目前要求 tcp
	Status       devices.VendorStatus   `json:"status"`        // enabled／disabled
	EnabledAt    *time.Time             `json:"enabled_at"`    // 最近启用时刻，未启用可空
	CreatedAt    time.Time              `json:"created_at"`    // 创建时间，UTC
	UpdatedAt    time.Time              `json:"updated_at"`    // 最近修改时间，UTC
}

// VendorFailure 保存可向调用方返回的 HTTP 状态、业务错误码和提示。
type VendorFailure struct {
	Status  int    // 可公开的 HTTP 400／404／409；依赖失败使用 503
	Code    int    // 业务码，依赖失败为 5003
	Message string // 已经由服务确定的公开提示
}

// Error 返回可向调用方公开的错误提示。
func (e *VendorFailure) Error() string { return e.Message }

// VendorRequest 提交厂商配置；服务端校验格式，禁止修改厂商编码，并检查协议能否更换。
type VendorRequest struct {
	VendorCode   string                 `json:"vendor_code"`   // 厂商编码，创建后不可改
	VendorName   string                 `json:"vendor_name"`   // 可见名称，服务端去空白并校验
	AdapterClass protocol.AdapterID     `json:"adapter_class"` // 适配器标签，原样落库
	Protocol     devices.VendorProtocol `json:"protocol"`      // tcp／mqtt／hybrid，网关检查是否支持该协议
	Status       devices.VendorStatus   `json:"status"`        // enabled／disabled
}

// VendorListRequest 指定厂商列表的分页和筛选条件。
type VendorListRequest struct {
	Page     int                  // 起始页码，从 1 开始
	PageSize int                  // 每页条数，1 至 100
	Keyword  string               // 搜索关键词；客户端仅 URL 编码，SQL LIKE 转义由 gateway 仓储处理
	Status   devices.VendorStatus // 零值（空）、enabled 或 disabled
	IDs      []uint64             // 当前账号的厂商范围，空表示不限定厂商
}

// VendorListReply 返回厂商列表和分页信息；账号权限由 central 校验。
type VendorListReply struct {
	Items    []VendorReply `json:"items"`     // 当前页的厂商列表，可为空
	Total    int64         `json:"total"`     // 当前筛选的总条数
	Page     int           `json:"page"`      // 响应页码
	PageSize int           `json:"page_size"` // 响应每页条数
}
