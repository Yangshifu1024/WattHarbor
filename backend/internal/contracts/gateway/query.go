package gateway

import (
	"encoding/json"
	"time"
)

const (
	TelemetryPath       = "/api/v1/internal/devices/"       // 后接转义后的设备编号与 /telemetry
	HistoricalCurvePath = "/api/v1/internal/devices/"       // 后接转义后的设备编号与 /historical-curve
	OrderProcessPath    = "/api/v1/internal/charge-orders/" // 后接转义业务订单号与 /process
	MaxQueryPoints      = 2000                              // 公开曲线与过程单页点数上限
)

// TelemetryRequest 指定设备端口和查询时间范围，不携用户凭据。
type TelemetryRequest struct {
	DeviceID string    // 设备编号
	PortNo   uint8     // 物理端口号，从 1 开始
	From     time.Time // 查询下界，UTC 时间
	To       time.Time // 查询上界，UTC 时间
}

// CurvePoint 保留计量单位；未上报指标为空，不补零或插值。
type CurvePoint struct {
	TS           string   `json:"ts"`            // 观测或聚合桶的 RFC3339 时间
	PowerW       *float64 `json:"power_w"`       // 功率，W，未上报为空
	CurrentA     *float64 `json:"current_a"`     // 电流，A，未上报为空
	VoltageV     *float64 `json:"voltage_v"`     // 电压，V，未上报为空
	TemperatureC *float64 `json:"temperature_c"` // 温度，摄氏度，未上报为空
	BatterySOC   *float64 `json:"battery_soc"`   // 电池荷电百分比，未上报为空
	MeterKWh     *float64 `json:"meter_kwh"`     // 累计电量，kWh，未上报为空
}

// TelemetryReply 是网关实际查询窗口及分辨率，网关可在配置上界内缩短请求窗口。
type TelemetryReply struct {
	DeviceID    string       `json:"device_id"`   // 设备编号
	From        time.Time    `json:"from"`        // 实际查询下界
	To          time.Time    `json:"to"`          // 实际查询上界
	Series      []CurvePoint `json:"series"`      // 按原顺序排列的观测或聚合点，无点为空数组
	Count       int          `json:"count"`       // 返回点数，等于 series 长度
	Granularity string       `json:"granularity"` // raw、15min或hourly
	Bucket      string       `json:"bucket"`      // 实际聚合桶时长文本
}

// HistoricalCurveRequest 使用已校验归属的订单起止时间，查询指定粒度的历史曲线。
type HistoricalCurveRequest struct {
	DeviceID    string    // 设备编号
	OrderNo     string    // 业务订单号，对应 order_id 查询字段
	PortNo      uint8     // 物理端口号，从 1 开始
	StartedAt   time.Time // 实际开始时刻
	EndedAt     time.Time // 结束时间；未结束时使用本次查询截止时间
	Granularity string    // 15min或hourly
}

// HistoricalCurveReply 返回实际查询范围、聚合边界和摘要；聚合点不能用于最终结算。
type HistoricalCurveReply struct {
	OrderNo             string                     `json:"order_id"`             // 业务订单号，JSON 字段名为 order_id
	DeviceID            string                     `json:"device_id"`            // 设备编号
	PortNo              uint8                      `json:"port_no"`              // 物理端口号
	StartedAt           time.Time                  `json:"started_at"`           // 请求指定的开始时间
	EndedAt             time.Time                  `json:"ended_at"`             // 请求指定的结束时间
	Granularity         string                     `json:"granularity"`          // 请求指定的聚合粒度
	Series              []CurvePoint               `json:"series"`               // 按原顺序排列的聚合点，无点为空数组
	Count               int                        `json:"count"`                // 返回点数，等于series长度
	Summary             map[string]json.RawMessage `json:"summary"`              // 摘要 JSON，保留数字文本精度与空值
	BoundaryApproximate bool                       `json:"boundary_approximate"` // 查询范围边界是否未对齐聚合时间桶
}

// OrderProcessRequest 指定已授权订单的设备、端口和分页游标，订单号不得替换。
type OrderProcessRequest struct {
	OrderID  uint64 // 中央订单 ID
	OrderNo  string // 业务订单号
	DeviceID string // 设备编号
	PortNo   uint8  // 物理端口号
	AfterID  uint64 // 上页最后一条记录的 ID，首次查询为 0
	Limit    uint16 // 单页点数，1–2000
}

// ProcessPoint 保存心跳读数；扩展数据未上报时为空，已上报的零值有效。
type ProcessPoint struct {
	ID               uint64    `json:"id"`                // 过程记录 ID，保留整数精度
	TS               time.Time `json:"ts"`                // 心跳接收时间
	PowerW           float64   `json:"power_w"`           // 功率，W
	ChargedKWh       float64   `json:"charged_kwh"`       // 累计电量，kWh
	RemainingKWh     float64   `json:"remaining_kwh"`     // 剩余电量，kWh
	ChargedSeconds   uint32    `json:"charged_seconds"`   // 累计时长，秒
	RemainingSeconds uint32    `json:"remaining_seconds"` // 剩余时长，秒
	SignalStrength   uint8     `json:"signal_strength"`   // 信号强度，零是有效报告
	PortStatus       *uint8    `json:"port_status"`       // 端口状态码，未上报为空
	VoltageV         *uint16   `json:"voltage_v"`         // 电压，V，扩展块缺失为空
	TemperatureC     *int16    `json:"temperature_c"`     // 温度，摄氏度，扩展块缺失为空
	DeviceStatus     *uint8    `json:"device_status"`     // 设备状态码，扩展块缺失为空
}

// OrderProcessReply 按记录 ID 分页；没有下一页时 next_after_id 为 null。
type OrderProcessReply struct {
	Items       []ProcessPoint `json:"items"`         // 按原顺序排列的过程点，无点为空数组
	NextAfterID *uint64        `json:"next_after_id"` // 下一页游标，末页为空
}
