package gateway

import (
	"encoding/json"
	"time"
)

const (
	TelemetryPath       = "/api/v1/internal/devices/"       // 后接转义设备身份与 /telemetry
	HistoricalCurvePath = "/api/v1/internal/devices/"       // 后接转义设备身份与 /historical-curve
	OrderProcessPath    = "/api/v1/internal/charge-orders/" // 后接转义业务订单号与 /process
	MaxQueryPoints      = 2000                              // 公开曲线与过程单页点数上限
)

// TelemetryRequest 是原设备端口的有界观测时间窗，不包含用户身份或授权凭据。
type TelemetryRequest struct {
	DeviceID string    // 原设备全局身份
	PortNo   uint8     // 原物理端口，从1开始
	From     time.Time // 查询下界，UTC时刻
	To       time.Time // 查询上界，UTC时刻
}

// CurvePoint 保留原读数单位；可空指标为未上报，不能补零或插值。
type CurvePoint struct {
	TS           string   `json:"ts"`            // 原观测／聚合桶的RFC3339时刻
	PowerW       *float64 `json:"power_w"`       // 功率，W，未上报为空
	CurrentA     *float64 `json:"current_a"`     // 电流，A，未上报为空
	VoltageV     *float64 `json:"voltage_v"`     // 电压，V，未上报为空
	TemperatureC *float64 `json:"temperature_c"` // 温度，摄氏度，未上报为空
	BatterySOC   *float64 `json:"battery_soc"`   // 电池荷电百分比，未上报为空
	MeterKWh     *float64 `json:"meter_kwh"`     // 原累计电量，kWh，未上报为空
}

// TelemetryReply 是网关实际查询窗口及分辨率，网关可在配置上界内缩短请求窗口。
type TelemetryReply struct {
	DeviceID    string       `json:"device_id"`   // 原设备全局身份
	From        time.Time    `json:"from"`        // 实际查询下界
	To          time.Time    `json:"to"`          // 实际查询上界
	Series      []CurvePoint `json:"series"`      // 原序观测或聚合点，无点为空数组
	Count       int          `json:"count"`       // 返回点数，等于 series 长度
	Granularity string       `json:"granularity"` // raw、15min或hourly
	Bucket      string       `json:"bucket"`      // 实际聚合桶时长文本
}

// HistoricalCurveRequest 的窗口由已授权订单事实确定，调用者只选择既定聚合粒度。
type HistoricalCurveRequest struct {
	DeviceID    string    // 原设备全局身份
	OrderNo     string    // 原业务订单号，对应原 order_id 查询字段
	PortNo      uint8     // 原物理端口，从1开始
	StartedAt   time.Time // 原实际开始时刻
	EndedAt     time.Time // 原结束时刻，未结束时使用本次受控上界
	Granularity string    // 15min或hourly
}

// HistoricalCurveReply 保留原窗口、聚合边界及摘要，不将聚合点当最终结算计量。
type HistoricalCurveReply struct {
	OrderNo             string                     `json:"order_id"`             // 原业务单号，内部字段名沿用order_id
	DeviceID            string                     `json:"device_id"`            // 原设备全局身份
	PortNo              uint8                      `json:"port_no"`              // 原物理端口
	StartedAt           time.Time                  `json:"started_at"`           // 原请求实际开始时刻
	EndedAt             time.Time                  `json:"ended_at"`             // 原请求受控结束时刻
	Granularity         string                     `json:"granularity"`          // 原请求聚合粒度
	Series              []CurvePoint               `json:"series"`               // 原序聚合点，无点为空数组
	Count               int                        `json:"count"`                // 返回点数，等于series长度
	Summary             map[string]json.RawMessage `json:"summary"`              // 原摘要JSON，保留数字文本精度与空值
	BoundaryApproximate bool                       `json:"boundary_approximate"` // 原窗口边界是否与聚合桶不对齐
}

// OrderProcessRequest 只接受所属订单的规范身份和原游标，不替换请求中的业务单号。
type OrderProcessRequest struct {
	OrderID  uint64 // 原中央订单主键
	OrderNo  string // 原业务订单号
	DeviceID string // 原设备全局身份
	PortNo   uint8  // 原物理端口
	AfterID  uint64 // 已消费的最后过程行主键，首读为0
	Limit    uint16 // 单页点数，1–2000
}

// ProcessPoint 保留网关原心跳过程读数，扩展块缺失为null，已上报零值仍有效。
type ProcessPoint struct {
	ID               uint64    `json:"id"`                // 原过程行主键，按原整数精度传递
	TS               time.Time `json:"ts"`                // 原心跳接收时刻
	PowerW           float64   `json:"power_w"`           // 原功率，W
	ChargedKWh       float64   `json:"charged_kwh"`       // 原累计电量，kWh
	RemainingKWh     float64   `json:"remaining_kwh"`     // 原剩余电量，kWh
	ChargedSeconds   uint32    `json:"charged_seconds"`   // 原累计时长，秒
	RemainingSeconds uint32    `json:"remaining_seconds"` // 原剩余时长，秒
	SignalStrength   uint8     `json:"signal_strength"`   // 原信号强度，零是有效报告
	PortStatus       *uint8    `json:"port_status"`       // 原端口状态码，未上报为空
	VoltageV         *uint16   `json:"voltage_v"`         // 原电压，V，扩展块缺失为空
	TemperatureC     *int16    `json:"temperature_c"`     // 原温度，摄氏度，扩展块缺失为空
	DeviceStatus     *uint8    `json:"device_status"`     // 原设备状态码，扩展块缺失为空
}

// OrderProcessReply 沿用原过程行主键分页；无下一页时next_after_id显式为null。
type OrderProcessReply struct {
	Items       []ProcessPoint `json:"items"`         // 原序过程点，无点为空数组
	NextAfterID *uint64        `json:"next_after_id"` // 下一页原游标，末页为空
}
