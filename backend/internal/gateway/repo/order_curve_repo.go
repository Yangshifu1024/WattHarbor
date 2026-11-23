package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	contract "chargepilot/internal/contracts/gateway"
	"gorm.io/gorm"
)

// OrderCurve 仅对指定订单、端口和时间窗内的 charge_process 做统计。
func OrderCurve(ctx context.Context, db *gorm.DB, q contract.OrderCurveRequest) (contract.OrderCurveReply, error) {
	result := contract.OrderCurveReply{
		Series:  make([]contract.CurvePoint, 0),
		Summary: make(map[string]json.RawMessage),
	}
	scoped := func() *gorm.DB {
		return db.WithContext(ctx).Table("charge_process").
			Where("order_no = ? AND charge_order_id = ? AND device_id = ? AND port_no = ? AND ts >= ? AND ts <= ?",
				q.OrderNo, q.OrderID, q.DeviceID, q.PortNo, q.From, q.To)
	}

	var count int64
	if !q.Latest {
		if err := scoped().Count(&count).Error; err != nil {
			return result, fmt.Errorf("统计订单采样点: %w", err)
		}
		result.SamplingIntervalSeconds = curveInterval(q.To.Unix()-q.From.Unix(), count, q.Granularity)
	}

	var rows []struct {
		TS                             time.Time
		PowerW, VoltageV, TemperatureC *float64
		MeterKWh                       *float64 `gorm:"column:meter_kwh"`
	}
	reading := scoped().Select("ts, power_deciwatts/10 AS power_w, voltage_v, temperature_c, charged_mwh/1000000 AS meter_kwh")
	switch {
	case q.Latest:
		reading = reading.Order("ts DESC, id DESC").Limit(1)
	case result.SamplingIntervalSeconds > 0:
		// 以请求下界对齐桶，在同一桶内取最后一笔累计读数。
		bucketed := scoped().Select(
			"ts, power_deciwatts, voltage_v, temperature_c, charged_mwh, "+
				"FLOOR(TIMESTAMPDIFF(SECOND, ?, ts)/?) AS bucket_no, "+
				"ROW_NUMBER() OVER (PARTITION BY FLOOR(TIMESTAMPDIFF(SECOND, ?, ts)/?) ORDER BY ts DESC, id DESC) AS sample_rank",
			q.From, result.SamplingIntervalSeconds, q.From, result.SamplingIntervalSeconds,
		)
		reading = db.WithContext(ctx).Table("(?) AS samples", bucketed).
			Select("MIN(ts) AS ts, AVG(power_deciwatts)/10 AS power_w, AVG(voltage_v) AS voltage_v, " +
				"AVG(temperature_c) AS temperature_c, MAX(CASE WHEN sample_rank=1 THEN charged_mwh END)/1000000 AS meter_kwh").
			Group("bucket_no").Order("bucket_no ASC").Limit(contract.MaxQueryPoints)
	default:
		reading = reading.Order("ts ASC, id ASC").Limit(contract.MaxQueryPoints)
	}
	if err := reading.Scan(&rows).Error; err != nil {
		return result, fmt.Errorf("读取订单采样: %w", err)
	}
	for _, row := range rows {
		result.Series = append(result.Series, contract.CurvePoint{
			TS:     row.TS.UTC().Format(time.RFC3339Nano),
			PowerW: row.PowerW, VoltageV: row.VoltageV, TemperatureC: row.TemperatureC, MeterKWh: row.MeterKWh,
		})
	}
	if q.Summary {
		var summary struct {
			MaxPowerW, AvgPowerW, MaxTemperatureC *string
		}
		if err := scoped().Select("CAST(MAX(power_deciwatts)/10 AS DECIMAL(18,6)) AS max_power_w, " +
			"CAST(AVG(power_deciwatts)/10 AS DECIMAL(18,6)) AS avg_power_w, " +
			"CAST(MAX(temperature_c) AS DECIMAL(18,6)) AS max_temperature_c").Scan(&summary).Error; err != nil {
			return result, fmt.Errorf("汇总订单采样: %w", err)
		}
		for key, value := range map[string]*string{
			"max_power_w": summary.MaxPowerW, "avg_power_w": summary.AvgPowerW, "max_temperature_c": summary.MaxTemperatureC,
		} {
			if value != nil {
				// 旧接口使用 JSON 字符串承载 DECIMAL。
				result.Summary[key] = json.RawMessage(fmt.Sprintf("%q", *value))
			}
		}
	}
	return result, nil
}

func curveInterval(span, count int64, granularity string) int64 {
	var requested int64
	switch granularity {
	case "15min":
		requested = 900
	case "hourly":
		requested = 3600
	}
	if requested == 0 && count <= contract.MaxQueryPoints {
		return 0
	}
	return max(requested, span/contract.MaxQueryPoints+1)
}
