//go:build integration

package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	contract "chargepilot/internal/contracts/gateway"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/testfixture"
	"github.com/google/uuid"
)

// TestQueryClientsMatchRegisteredGateway 验证实际路由及真实 MySQL 的单位、端口隔离、空值、摘要与 64 位分页。
// 订单过程数据仅在本测试外层事务中可见并回滚；服务令牌失败不变成空数据。
func TestQueryClientsMatchRegisteredGateway(t *testing.T) {
	tx := gatewayContractDB(t).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	tag := uuid.NewString()
	device := "QC" + tag
	no := "QO" + tag
	id := testfixture.NewID()
	to := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	from := to.Add(-10 * time.Minute)
	for i := range 3 {
		row := map[string]any{"id": id + uint64(i), "event_key": fmt.Sprintf("%s-%d", tag, i), "charge_order_id": id, "order_no": no, "device_id": device, "port_no": 2, "ts": from.Add(time.Duration(i) * time.Second), "power_deciwatts": 1485, "charged_seconds": 30 + i, "remaining_seconds": 120 - i, "charged_mwh": 2500, "remaining_mwh": 1250000, "signal_strength": 0}
		if i == 1 {
			row["power_deciwatts"] = 0
		}
		if i == 2 {
			row["power_deciwatts"] = 3000
		}
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
	DeviceEventAPI{DB: tx, ServiceToken: "actual-query-contract"}.Register(router)
	for _, retired := range []struct{ method, suffix string }{
		{http.MethodGet, "/telemetry"},
		{http.MethodGet, "/historical-curve"},
		{http.MethodPost, "/backfill"},
	} {
		path := "/api/v1/internal/devices/" + device + retired.suffix
		request := httptest.NewRequest(retired.method, path, nil)
		request.Header.Set("X-Service-Token", "actual-query-contract")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("%s %s: status=%d, want 404", retired.method, path, response.Code)
		}
	}
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "actual-query-contract")
	if err != nil {
		t.Fatal(err)
	}
	q := contract.OrderProcessRequest{OrderID: id, OrderNo: no, DeviceID: device, PortNo: 2, AfterID: id - 1, Limit: 2}
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
	curveQuery := contract.OrderCurveRequest{
		OrderID: id, OrderNo: no, DeviceID: device, PortNo: 2, From: from, To: to, Granularity: "auto",
	}
	if _, err := bad.OrderCurve(context.Background(), curveQuery); err == nil {
		t.Fatal("rejected token became empty curve")
	}

	curve, err := client.OrderCurve(context.Background(), curveQuery)
	if err != nil || len(curve.Series) != 3 || curve.Series[1].PowerW == nil || *curve.Series[1].PowerW != 0 ||
		curve.Series[0].MeterKWh == nil || *curve.Series[0].MeterKWh != 0.0025 || curve.Series[1].VoltageV != nil {
		t.Fatalf("registered order curve=%+v %v", curve, err)
	}
	curveQuery.Granularity = "15min"
	curveQuery.Summary = true
	history, err := client.OrderCurve(context.Background(), curveQuery)
	if err != nil || len(history.Series) != 1 || history.SamplingIntervalSeconds != 900 {
		t.Fatalf("registered aggregated order curve=%+v %v", history, err)
	}
	var average string
	if err := json.Unmarshal(history.Summary["avg_power_w"], &average); err != nil {
		t.Fatal("average DECIMAL missing", err)
	}
	if value, err := strconv.ParseFloat(average, 64); err != nil || value != 149.5 {
		t.Fatal("order average changed", average, err)
	}
}
