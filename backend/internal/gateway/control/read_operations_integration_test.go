//go:build integration

package control

import (
	"context"
	"errors"
	"testing"
	"time"

	gatewayrepo "github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
	"github.com/google/uuid"
)

// TestTelemetryReadOperationsPreserveWindowAndDeviceMetrics 验证真实 MySQL 的闭区间、设备级指标和八倍行预算。
// 原始与两种聚合数据只在外层事务中可见并回滚，不修改已有设备或资金数据。
func TestTelemetryReadOperationsPreserveWindowAndDeviceMetrics(t *testing.T) {
	tx := gatewayContractDB(t).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	device := "READ-" + uuid.NewString()
	from := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	to := from.Add(9 * time.Hour)
	for _, table := range []string{"telemetry", "telemetry_aggregate_15min", "telemetry_aggregate_hourly"} {
		t.Run(table, func(t *testing.T) {
			insert := func(device string, ts time.Time, port any, metric string, value any) {
				t.Helper()
				row := map[string]any{"device_id": device, "port_no": port, "metric": metric}
				if table == "telemetry" {
					row["ts"], row["value_num"] = ts, value
				} else {
					row["bucket_start"], row["bucket_month"] = ts, ts.Format("2006-01")+"-01"
					row["avg_value"], row["min_value"], row["max_value"], row["count"] = value, value, value, 1
				}
				if err := tx.Table(table).Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			list := func(device string, limit int) ([]gatewayrepo.Reading, error) {
				if table == "telemetry" {
					return gatewayrepo.ListRawReadings(ctx, tx, device, 2, from, to, limit)
				}
				return gatewayrepo.ListBucketedReadings(ctx, tx, table, device, 2, from, to, limit)
			}
			insert(device, from, 2, "power_w", "0")
			insert(device, to, nil, "voltage_v", "220")
			insert(device, to, nil, "temperature_c", "-2")
			insert(device, to, nil, "power_w", "9999")
			insert(device, to, 1, "current_a", "9999")
			insert(device, from.Add(-time.Millisecond), 2, "power_w", "9999")
			insert(device, to.Add(time.Millisecond), 2, "power_w", "9999")
			insert(device+"other", to, 2, "power_w", "9999")
			if table == "telemetry" {
				insert(device, to, 2, "meter_kwh", nil)
			}
			rows, err := list(device, 10)
			if err != nil || len(rows) != 3 || !rows[0].TS.Equal(to) || !rows[2].TS.Equal(from) {
				t.Fatalf("boundary/port selection: rows=%+v err=%v", rows, err)
			}
			series, err := (TelemetryAPI{DB: tx}).readPort(ctx, device, 2, from, to, 10, table)
			if err != nil || len(series) != 2 {
				t.Fatalf("curve conversion: series=%+v err=%v", series, err)
			}
			first, last := series[0], series[1]
			if first.TS != from.Format(time.RFC3339) || first.PowerW == nil || *first.PowerW != 0 || last.TS != to.Format(time.RFC3339) || last.PowerW != nil || last.CurrentA != nil || last.MeterKWh != nil || last.VoltageV == nil || *last.VoltageV != 220 || last.TemperatureC == nil || *last.TemperatureC != -2 {
				t.Fatalf("device metrics/zero/NULL/ascending curve changed: %+v", series)
			}
			budgetDevice := device + "budget"
			for i := range 10 {
				insert(budgetDevice, from.Add(time.Duration(i)*time.Hour), 2, "power_w", i)
			}
			rows, err = list(budgetDevice, 1)
			if err != nil || len(rows) != 8 {
				t.Fatalf("eight-row budget: rows=%+v err=%v", rows, err)
			}
			for i, row := range rows {
				if !row.TS.Equal(to.Add(-time.Duration(i) * time.Hour)) {
					t.Fatalf("latest rows/order changed at %d: %+v", i, row)
				}
			}
			series, err = (TelemetryAPI{DB: tx}).readPort(ctx, budgetDevice, 2, from, to, 1, table)
			if err != nil || len(series) != 1 || series[0].TS != to.Format(time.RFC3339) || series[0].PowerW == nil || *series[0].PowerW != 9 {
				t.Fatalf("latest curve point budget: %+v err=%v", series, err)
			}
			rows, err = list(device+"missing", 10)
			if err != nil || rows == nil || len(rows) != 0 {
				t.Fatalf("empty read became nil/error: %+v err=%v", rows, err)
			}
		})
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"telemetry", "telemetry_aggregate_15min", "telemetry_aggregate_hourly"} {
		var remaining int64
		if err := gatewayContractDB(t).WithContext(ctx).Table(table).Where("device_id IN ?", []string{device, device + "budget", device + "other"}).Count(&remaining).Error; err != nil || remaining != 0 {
			t.Fatalf("fixture rollback %s: remaining=%d err=%v", table, remaining, err)
		}
	}
}

// TestGatewayReadOperationsHonorCanceledContext 验证三个完整读取返回真实取消错误，不能把取消当作空数据成功。
func TestGatewayReadOperationsHonorCanceledContext(t *testing.T) {
	db := gatewayContractDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	from := time.Now().UTC().Add(-time.Hour)
	to := from.Add(time.Hour)
	_, rawErr := gatewayrepo.ListRawReadings(ctx, db, "canceled", 2, from, to, 1)
	_, bucketErr := gatewayrepo.ListBucketedReadings(ctx, db, "telemetry_aggregate_15min", "canceled", 2, from, to, 1)
	_, processErr := gatewayrepo.ListChargeProcessPoints(ctx, db, "canceled", 0, "", 0, 0, 1)
	for name, err := range map[string]error{"raw": rawErr, "bucket": bucketErr, "process": processErr} {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s cancellation became success or changed error: %v", name, err)
		}
	}
}
