//go:build integration

package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/google/uuid"
)

// TestQueryClientsMatchRegisteredGateway 验证实际路由及真实MySQL的原单位、端口隔离、空值、摘要与64位分页。
// 原始／聚合／过程数据仅在本测试外层事务中可见并回滚；服务令牌失败不变成空数据。
func TestQueryClientsMatchRegisteredGateway(t *testing.T) {
	tx := gatewayContractDB(t).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	tag := uuid.NewString()
	device := "QC" + tag
	no := "QO" + tag
	id := uint64(time.Now().UnixNano())
	to := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	from := to.Add(-10 * time.Minute)
	for _, metric := range []struct {
		name  string // 原指标名
		value string // 原DECIMAL文本
	}{{"power_w", "0"}, {"meter_kwh", "0.0025"}, {"temperature_c", "-2"}} {
		if err := tx.Table("telemetry").Create(map[string]any{"device_id": device, "port_no": 2, "metric": metric.name, "value_num": metric.value, "ts": from.Add(time.Minute)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Table("telemetry").Create(map[string]any{"device_id": device, "port_no": 1, "metric": "power_w", "value_num": 9999, "ts": from.Add(time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	bucket := from.Truncate(15 * time.Minute)
	month := bucket.Format("2006-01") + "-01"
	if err := tx.Table("telemetry_aggregate_15min").Create(map[string]any{"device_id": device, "port_no": 2, "metric": "power_w", "bucket_start": bucket, "bucket_month": month, "avg_value": "150.125", "min_value": "100.25", "max_value": "200.375", "count": 3}).Error; err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		row := map[string]any{"id": id + uint64(i), "event_key": fmt.Sprintf("%s-%d", tag, i), "charge_order_id": id, "order_no": no, "device_id": device, "port_no": 2, "ts": from.Add(time.Duration(i) * time.Second), "power_deciwatts": 1485, "charged_seconds": 30 + i, "remaining_seconds": 120 - i, "charged_mwh": 2500, "remaining_mwh": 1250000, "signal_strength": 0}
		if i == 0 {
			row["port_status"] = 0
			row["voltage_v"] = 220
			row["temperature_c"] = -2
		}
		if err := tx.Table("charge_process").Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	router := httpapi.NewRouter()
	TelemetryAPI{DB: tx, ServiceToken: "actual-query-contract"}.Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "actual-query-contract")
	if err != nil {
		t.Fatal(err)
	}
	data, err := client.Telemetry(context.Background(), contract.TelemetryQuery{DeviceID: device, PortNo: 2, From: from, To: to})
	if err != nil || data.Count != 1 || data.Series[0].PowerW == nil || *data.Series[0].PowerW != 0 || data.Series[0].MeterKWh == nil || *data.Series[0].MeterKWh != 0.0025 || data.Series[0].VoltageV != nil {
		t.Fatalf("registered telemetry=%+v %v", data, err)
	}
	history, err := client.HistoricalCurve(context.Background(), contract.HistoricalCurveQuery{DeviceID: device, OrderNo: no, PortNo: 2, StartedAt: from, EndedAt: to, Granularity: "15min"})
	if err != nil || history.Count != 1 || !history.BoundaryApproximate || history.Series[0].PowerW == nil || *history.Series[0].PowerW != 150.125 {
		t.Fatalf("registered history=%+v %v", history, err)
	}
	var peak string
	if err := json.Unmarshal(history.Summary["max_power_w"], &peak); err != nil || !strings.HasPrefix(peak, "200.375") {
		t.Fatal("summary original DECIMAL string changed", peak, err)
	}
	var average string
	if err := json.Unmarshal(history.Summary["avg_power_w"], &average); err != nil {
		t.Fatal("original average DECIMAL missing", err)
	}
	if value, err := strconv.ParseFloat(average, 64); err != nil || value != 150.125 {
		t.Fatal("original average column mapping lost", average, err)
	}
	q := contract.OrderProcessQuery{OrderID: id, OrderNo: no, DeviceID: device, PortNo: 2, AfterID: id - 1, Limit: 2}
	page, err := client.OrderProcess(context.Background(), q)
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != id || page.NextAfterID == nil || *page.NextAfterID != id+1 {
		t.Fatalf("registered process=%+v %v", page, err)
	}
	p := page.Items[0]
	if p.PowerW != 148.5 || p.ChargedKWh != 0.0025 || p.RemainingKWh != 1.25 || p.PortStatus == nil || *p.PortStatus != 0 || p.TemperatureC == nil || *p.TemperatureC != -2 || p.DeviceStatus != nil || page.Items[1].VoltageV != nil {
		t.Fatal("registered process units or nulls changed")
	}
	q.AfterID = *page.NextAfterID
	page, err = client.OrderProcess(context.Background(), q)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != id+2 || page.NextAfterID != nil {
		t.Fatal("last page lost integer identity", page, err)
	}
	q.OrderID++
	page, err = client.OrderProcess(context.Background(), q)
	if err != nil || page.Items == nil || len(page.Items) != 0 || page.NextAfterID != nil {
		t.Fatal("canonical identity filter leaked another order", page, err)
	}
	bad, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "wrong-query-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Telemetry(context.Background(), contract.TelemetryQuery{DeviceID: device, PortNo: 2, From: from, To: to}); err == nil {
		t.Fatal("rejected token became empty telemetry")
	}
	// 第二个桶的样本数不同，原摘要必须按count加权，不能用桶均值的简单平均。
	previous := bucket.Add(-15 * time.Minute)
	if err := tx.Table("telemetry_aggregate_15min").Create(map[string]any{"device_id": device, "port_no": 2, "metric": "power_w", "bucket_start": previous, "bucket_month": previous.Format("2006-01") + "-01", "avg_value": "300", "min_value": "200", "max_value": "400", "count": 1}).Error; err != nil {
		t.Fatal(err)
	}
	weighted, err := client.HistoricalCurve(context.Background(), contract.HistoricalCurveQuery{DeviceID: device, OrderNo: no, PortNo: 2, StartedAt: previous, EndedAt: to, Granularity: "15min"})
	if err != nil || weighted.Count != 2 {
		t.Fatal("registered weighted summary unavailable", err)
	}
	if err := json.Unmarshal(weighted.Summary["avg_power_w"], &average); err != nil {
		t.Fatal(err)
	}
	if value, err := strconv.ParseFloat(average, 64); err != nil || value != 187.59375 {
		t.Fatal("registered summary lost sample weighting", average, err)
	}
}
