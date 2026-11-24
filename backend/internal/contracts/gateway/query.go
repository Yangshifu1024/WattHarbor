package gateway

import (
	"encoding/json"
	"time"
)

const (
	OrderProcessPath    = "/api/v1/internal/charge-orders/" // 后接转义业务订单号与 /process
	MaxQueryPoints      = 2000                              // 公开曲线与过程单页点数上限
	MaxOrderCurveWindow = 3 * 366 * 24 * time.Hour          // 单次查询的订单跨度上限，涵盖闰年
)

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

// OrderCurveRequest 限制曲线为已授权订单的完整身份及时间窗。
type OrderCurveRequest struct {
	OrderNo     string
	OrderID     uint64
	DeviceID    string
	PortNo      uint8
	From        time.Time
	To          time.Time
	Granularity string // auto、15min、hourly
	Latest      bool
	Summary     bool
}

// OrderCurveReply 保留实际桶宽。原始样本的采样间隔为 0。
type OrderCurveReply struct {
	Series                  []CurvePoint               `json:"series"`
	Summary                 map[string]json.RawMessage `json:"summary"`
	SamplingIntervalSeconds int64                      `json:"sampling_interval_seconds"`
}
