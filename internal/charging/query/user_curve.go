package query

import (
	"context"
	"encoding/json"
	"errors"
	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"strings"
	"time"
)

// CurvePoint 是本用例遥测点，保留原单位、可空读数及 UTC 时刻，不作为计费证据。
type CurvePoint struct {
	TS           time.Time `json:"ts"`            // 原观测或聚合桶 UTC 时刻
	PowerW       *float64  `json:"power_w"`       // 原功率 W，未上报为空
	CurrentA     *float64  `json:"current_a"`     // 原电流 A，未上报为空
	VoltageV     *float64  `json:"voltage_v"`     // 原电压 V，未上报为空
	TemperatureC *float64  `json:"temperature_c"` // 原摄氏度，未上报为空
	BatterySOC   *float64  `json:"battery_soc"`   // 原荷电百分比，未上报为空
	MeterKWh     *float64  `json:"meter_kwh"`     // 原累计 kWh，未上报为空
}

// HistoricalPoint 保持历史接口原时刻文本与可空字段，不直接公开网关契约类型。
type HistoricalPoint struct {
	TS           string   `json:"ts"`            // 原桶的 RFC3339 时刻文本
	PowerW       *float64 `json:"power_w"`       // 原功率 W
	CurrentA     *float64 `json:"current_a"`     // 原电流 A
	VoltageV     *float64 `json:"voltage_v"`     // 原电压 V
	TemperatureC *float64 `json:"temperature_c"` // 原摄氏度
	BatterySOC   *float64 `json:"battery_soc"`   // 原荷电百分比
	MeterKWh     *float64 `json:"meter_kwh"`     // 原累计 kWh
}

// CurveView 是实时曲线公开读模型；窗口上界只捕获一次，系列为空时仍输出数组。
type CurveView struct {
	OrderNo string       `json:"order_no"` // 已授权原业务单号
	Window  string       `json:"window"`   // 已允许的回看枚举
	From    time.Time    `json:"from"`     // 本次请求窗口下界 UTC
	To      time.Time    `json:"to"`       // 本次请求窗口上界 UTC
	Series  []CurvePoint `json:"series"`   // 原序观测，不插值或补零
	Count   int          `json:"count"`    // 原返回点数
}

// HistoricalView 保留原订单身份、聚合边界与摘要，确认电量以原 DECIMAL 文本优先。
type HistoricalView struct {
	OrderID             uint64                     `json:"order_id"`             // 原中央订单主键，保持现有整数契约
	OrderNo             string                     `json:"order_no"`             // 已授权原业务单号
	Granularity         string                     `json:"granularity"`          // 原请求15min或hourly
	Series              []HistoricalPoint          `json:"series"`               // 原序聚合点，不充当最终计费证据
	Summary             map[string]json.RawMessage `json:"summary"`              // 原摘要JSON，保留数字精度及空值
	BoundaryApproximate bool                       `json:"boundary_approximate"` // 原聚合边界不完全对齐事实
}

// windowDuration 对 raw 与 allowed 核对既定窗口，返回规范标签、时长或公开准入错误，无IO。
// 空输入使用首个配置窗口；未知或禁用值不再静默替换，所有允许值仍最多24小时。
func windowDuration(raw string, allowed []string) (string, time.Duration, error) {
	if len(allowed) == 0 {
		allowed = []string{"last_30min", "last_2h", "last_24h"}
	}
	durations := map[string]time.Duration{"last_30min": 30 * time.Minute, "last_2h": 2 * time.Hour, "last_24h": 24 * time.Hour}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = allowed[0]
	}
	d, ok := durations[raw]
	if ok {
		for _, w := range allowed {
			if w == raw {
				return raw, d, nil
			}
		}
	}
	return "", 0, &UserInputError{Kind: "input", Message: "window 须为允许的 last_30min、last_2h 或 last_24h"}
}

// Curve 在 ctx 下先核对 userID 对 raw 的原订单归属，再按 window 允许窗口读取原端口观测。
// 返回私有曲线或准入／缺行／依赖错误；网关失败不伪造空数组，不执行资金写入。
func (s UserService) Curve(ctx context.Context, userID uint64, raw, window string) (CurveView, error) {
	row, err := s.Orders.Resolve(ctx, userID, raw)
	if err != nil {
		return CurveView{}, err
	}
	label, duration, err := windowDuration(window, s.AllowedWindows)
	if err != nil {
		return CurveView{}, err
	}
	now := s.now()
	from := now.Add(-duration)
	out := CurveView{OrderNo: row.OrderNo, Window: label, From: from, To: now, Series: []CurvePoint{}}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if s.Gateway == nil {
		return out, &UserReadError{Stage: "curve", Cause: errors.New("gateway reader unavailable")}
	}
	data, err := s.Gateway.Telemetry(ctx, contract.TelemetryRequest{DeviceID: row.DeviceID, PortNo: row.PortNo, From: from, To: now})
	if err != nil {
		return out, &UserReadError{Stage: "curve", Cause: err}
	}
	for _, p := range data.Series {
		at, err := time.Parse(time.RFC3339Nano, p.TS)
		if err != nil {
			return out, &UserReadError{Stage: "curve", Cause: err}
		}
		out.Series = append(out.Series, CurvePoint{TS: at.UTC(), PowerW: p.PowerW, CurrentA: p.CurrentA, VoltageV: p.VoltageV, TemperatureC: p.TemperatureC, BatterySOC: p.BatterySOC, MeterKWh: p.MeterKWh})
	}
	out.Count = len(out.Series)
	return out, nil
}

// HistoricalCurve 在 ctx 下先核对 userID 的 raw 原订单，再用已冻结开始／结束时刻和 granularity 读取聚合。
// 返回本用例模型或原409／422／点数准入／依赖错误；三年与2000点上限在发起外部请求前核对。
// 摘要确认电量来自原订单 DECIMAL，不从实时观测重算，聚合边界标志保持原值。
func (s UserService) HistoricalCurve(ctx context.Context, userID uint64, raw, granularity string) (HistoricalView, error) {
	row, err := s.Orders.Resolve(ctx, userID, raw)
	if err != nil {
		return HistoricalView{}, err
	}
	if !row.StartedAt.Valid {
		return HistoricalView{}, &UserInputError{Kind: "conflict", Message: "订单尚未开始充电"}
	}
	if granularity == "" {
		granularity = "15min"
	}
	if granularity != "15min" && granularity != "hourly" {
		return HistoricalView{}, &UserInputError{Kind: "input", Message: "granularity 须为 15min 或 hourly"}
	}
	now := s.now()
	start := row.StartedAt.Time.UTC()
	end := now
	if row.EndedAt.Valid {
		end = row.EndedAt.Time.UTC()
	}
	if !start.Before(end) {
		return HistoricalView{}, &UserInputError{Kind: "conflict", Message: "订单时间窗无效"}
	}
	if start.Before(now.AddDate(-3, 0, 0)) {
		return HistoricalView{}, &UserInputError{Kind: "expired", Message: "历史遥测已超过三年保留期"}
	}
	bucket := 15 * time.Minute
	if granularity == "hourly" {
		bucket = time.Hour
	}
	if end.Truncate(bucket).Sub(start.Truncate(bucket))/bucket+1 > contract.MaxQueryPoints {
		return HistoricalView{}, &UserInputError{Kind: "input", Message: "订单时间窗超过当前粒度的最大点数，请使用更粗粒度"}
	}
	out := HistoricalView{OrderID: row.ID, OrderNo: row.OrderNo, Granularity: granularity, Series: []HistoricalPoint{}, Summary: map[string]json.RawMessage{}}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if s.Gateway == nil {
		return out, &UserReadError{Stage: "history_curve", Cause: errors.New("gateway reader unavailable")}
	}
	data, err := s.Gateway.HistoricalCurve(ctx, contract.HistoricalCurveRequest{DeviceID: row.DeviceID, OrderNo: row.OrderNo, PortNo: row.PortNo, StartedAt: start, EndedAt: end, Granularity: granularity})
	if err != nil {
		return out, &UserReadError{Stage: "history_curve", Cause: err}
	}
	for _, p := range data.Series {
		out.Series = append(out.Series, HistoricalPoint(p))
	}
	for key, value := range data.Summary {
		out.Summary[key] = value
	}
	if row.ChargedKWh.Valid {
		value, _ := json.Marshal(row.ChargedKWh.String)
		out.Summary["total_kwh"] = value
	}
	out.BoundaryApproximate = data.BoundaryApproximate
	return out, nil
}
