//go:build integration

package control

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/google/uuid"
)

// 实际路由与真实 SQL 验证同步契约，所有测试写入和端点事务位于外层回滚事务中。
func TestWorkerSyncClientMatchesRegisteredGatewayAndMySQL(t *testing.T) {
	db := gatewayContractDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	id := uint64(time.Now().UnixNano())
	device := "SYNC-CONTRACT-" + rand.Text()
	orderNo := "C-SYNC-" + rand.Text()
	commandID, stopID := uuid.NewString(), uuid.NewString()
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	end := start.Add(30 * time.Minute)
	for _, seed := range []struct {
		table string
		row   map[string]any
	}{
		{"charge_command", map[string]any{"command_id": commandID, "stop_command_id": stopID, "charge_order_id": id, "payment_order_id": id, "order_no": orderNo, "user_id": id, "device_id": device, "port_no": 1, "port_code": device, "port_id": nil, "status": "acked", "result_code": 0, "ack_at": start}},
		{"device_port", map[string]any{"id": id, "device_id": device, "port_no": 1, "port_code": device, "status": "charging", "current_order_id": orderNo}},
		{"device_event", map[string]any{"id": id, "event_key": uuid.NewString(), "protocol_name": "dc589", "device_id": device, "event_type": "heartbeat", "event_json": `{"Type":"heartbeat","DeviceID":"` + device + `"}`, "received_at": start.Add(time.Minute)}},
		{"device_event", map[string]any{"id": id + 1, "event_key": uuid.NewString(), "protocol_name": "dc589", "device_id": device, "event_type": "charge_end", "event_json": `{"ConsumerType":2}`, "received_at": end}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(err)
		}
	}
	router := httpapi.NewRouter()
	(TelemetryAPI{DB: tx, ServiceToken: "secret"}).Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := client.StartResults(ctx, commandID)
	if err != nil || len(rows) != 1 || rows[0].PortID != nil || rows[0].ResultCode == nil || *rows[0].ResultCode != 0 || !rows[0].AckAt.Equal(start) {
		t.Fatalf("results=%+v err=%v", rows, err)
	}
	command, err := client.ChargeCommand(ctx, id)
	if err != nil || command == nil || command.ChargeOrderID != id || command.PortID != nil {
		t.Fatalf("command=%+v err=%v", command, err)
	}
	samples, err := client.MeterSamples(ctx, gatewaycontract.MeterSamplesQuery{DeviceID: device, AckAt: start, EndAt: end, EndID: id + 1})
	if err != nil || len(samples) != 1 || samples[0].ID != id {
		t.Fatalf("samples=%+v err=%v", samples, err)
	}
	events, err := client.EndEvents(ctx)
	if err != nil || events == nil {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	for _, event := range events {
		if event.ID == 0 || event.Payload == "" {
			t.Fatalf("invalid event: %+v", event)
		}
	}
	frozen := centralcontract.EndResult{OrderNo: orderNo, ChargeOrderID: id, StartCommandID: commandID, StopCommandID: stopID, DeviceID: device, PortNo: 1, PortID: id, Meter: charging.EndMeter{ChargedWh: 1000, ChargedSeconds: 1800, EndedAt: end, Segments: []charging.MeterSegment{{StartedAt: start, EndedAt: end, EnergyWh: 1000}}}}
	data, _ := json.Marshal(frozen)
	request := gatewaycontract.FreezeEndDeliveryRequest{DeviceEventID: id + 1, ChargeOrderID: id, Payload: string(data)}
	first, err := client.FreezeEndDelivery(ctx, request)
	if err != nil || first.Frozen {
		t.Fatalf("first freeze=%+v err=%v", first, err)
	}
	candidate := frozen
	candidate.Meter.Segments = nil
	data, _ = json.Marshal(candidate)
	request.Payload = string(data)
	second, err := client.FreezeEndDelivery(ctx, request)
	if err != nil || !second.Frozen {
		t.Fatalf("replay freeze=%+v err=%v", second, err)
	}
	var original, replayed centralcontract.EndResult
	if json.Unmarshal([]byte(first.Payload), &original) != nil || json.Unmarshal([]byte(second.Payload), &replayed) != nil || !reflect.DeepEqual(original, replayed) || len(replayed.Meter.Segments) != 1 {
		t.Fatalf("frozen evidence changed: %+v %+v", original, replayed)
	}
	candidate.ChargeOrderID++
	data, _ = json.Marshal(candidate)
	request.ChargeOrderID, request.Payload = candidate.ChargeOrderID, string(data)
	_, err = client.FreezeEndDelivery(ctx, request)
	var freezeConflict *serviceclient.HTTPError
	if !errors.As(err, &freezeConflict) || freezeConflict.StatusCode != http.StatusConflict {
		t.Fatalf("different large order ID accepted as a frozen replay: %v", err)
	}
	release := gatewaycontract.ReleasePortRequest{PortID: id, OrderNo: orderNo + "-other"}
	_, err = client.ReleasePort(ctx, release)
	var rejection *serviceclient.HTTPError
	if !errors.As(err, &rejection) || rejection.StatusCode != http.StatusConflict {
		t.Fatalf("wrong owner released: %v", err)
	}
	release.OrderNo = orderNo
	for i := range 2 {
		result, err := client.ReleasePort(ctx, release)
		if err != nil || !result.Released || result.Already != (i == 1) {
			t.Fatalf("release=%+v err=%v", result, err)
		}
	}
	for i := range 2 {
		marked, err := client.MarkStartResultsReported(ctx, []string{commandID})
		if err != nil || marked != 1-i {
			t.Fatalf("start mark=%d err=%v", marked, err)
		}
	}
	for i := range 2 {
		marked, err := client.MarkDeviceEventsProcessed(ctx, []uint64{id + 1})
		if err != nil || marked != 1-i {
			t.Fatalf("event mark=%d err=%v", marked, err)
		}
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		table, where string
		value        any
	}{
		{"charge_command", "command_id = ?", commandID}, {"device_port", "id = ?", id}, {"device_event", "device_id = ?", device}, {"charge_end_delivery", "device_event_id = ?", id + 1},
	} {
		var count int64
		if err := db.Table(check.table).Where(check.where, check.value).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("residual test data: %s count=%d err=%v", check.table, count, err)
		}
	}
}
