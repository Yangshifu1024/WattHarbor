package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/platform/serviceclient"
)

// TestQueryClientsKeepIdentityUnitsNullAndIntegerPrecision 用loopback信封验证原身份、单位、空值和64位游标精度，不代表真实网关取数。
func TestQueryClientsKeepIdentityUnitsNullAndIntegerPrecision(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 123000000, time.UTC)
	to := from.Add(time.Hour)
	device := "board&+一"
	client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Header.Get("X-Service-Token") != "test-service-token" || q.Get("port_no") != "2" {
			t.Errorf("original query identity lost: %s", r.URL)
		}
		switch r.URL.Path {
		case "/api/v1/internal/devices/" + device + "/telemetry":
			if q.Get("from") != from.Format(time.RFC3339Nano) || q.Get("to") != to.Format(time.RFC3339Nano) {
				t.Error("telemetry time lost")
			}
			zero := float64(0)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": contract.TelemetryReply{DeviceID: device, From: from, To: to, Series: []contract.CurvePoint{{TS: from.Format(time.RFC3339Nano), PowerW: &zero}}, Count: 1, Granularity: "raw", Bucket: "0s"}})
		case "/api/v1/internal/devices/" + device + "/historical-curve":
			if q.Get("order_id") != "CH&+原单" || q.Get("granularity") != "15min" {
				t.Error("historical identity lost")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": contract.HistoricalCurveReply{OrderNo: "CH&+原单", DeviceID: device, PortNo: 2, StartedAt: from, EndedAt: to, Granularity: "15min", Series: []contract.CurvePoint{}, Summary: map[string]json.RawMessage{"original_id": json.RawMessage("18446744073709551614")}, BoundaryApproximate: true}})
		case "/api/v1/internal/charge-orders/CH&+原单/process":
			if q.Get("charge_order_id") != "18446744073709551613" || q.Get("device_id") != device || q.Get("after_id") != "18446744073709551612" || q.Get("limit") != "2" {
				t.Error("process identity precision lost")
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":18446744073709551614,"ts":"2026-10-01T00:00:00.123Z","power_w":148.5,"charged_kwh":0.0025,"remaining_kwh":1.25,"charged_seconds":30,"remaining_seconds":120,"signal_strength":0,"port_status":0,"voltage_v":220,"temperature_c":-2,"device_status":null}],"next_after_id":18446744073709551614}}`))
		default:
			t.Errorf("unexpected query path %s", r.URL.Path)
		}
	})
	telemetry, err := client.Telemetry(context.Background(), contract.TelemetryRequest{DeviceID: device, PortNo: 2, From: from, To: to})
	if err != nil || len(telemetry.Series) != 1 || telemetry.Series[0].PowerW == nil || *telemetry.Series[0].PowerW != 0 || telemetry.Series[0].VoltageV != nil {
		t.Fatalf("nullable telemetry=%+v %v", telemetry, err)
	}
	history, err := client.HistoricalCurve(context.Background(), contract.HistoricalCurveRequest{DeviceID: device, OrderNo: "CH&+原单", PortNo: 2, StartedAt: from, EndedAt: to, Granularity: "15min"})
	if err != nil || history.Series == nil || !history.BoundaryApproximate || string(history.Summary["original_id"]) != "18446744073709551614" {
		t.Fatalf("history summary=%+v %v", history, err)
	}
	page, err := client.OrderProcess(context.Background(), contract.OrderProcessRequest{OrderID: 18446744073709551613, OrderNo: "CH&+原单", DeviceID: device, PortNo: 2, AfterID: 18446744073709551612, Limit: 2})
	if err != nil || len(page.Items) != 1 || page.NextAfterID == nil || *page.NextAfterID != 18446744073709551614 {
		t.Fatalf("process cursor=%+v %v", page, err)
	}
	p := page.Items[0]
	if p.PowerW != 148.5 || p.ChargedKWh != 0.0025 || p.RemainingKWh != 1.25 || p.PortStatus == nil || *p.PortStatus != 0 || p.TemperatureC == nil || *p.TemperatureC != -2 || p.DeviceStatus != nil || p.TS.Nanosecond() != 123000000 {
		t.Fatalf("raw process changed: %+v", p)
	}
}

// TestQueryClientsRejectMalformedDataAndRespectCancellation 验证缺字段、业务错误、坏游标、重定向及取消不伪装成空页或泄露令牌。
func TestQueryClientsRejectMalformedDataAndRespectCancellation(t *testing.T) {
	for _, tc := range []struct {
		name  string // 故障身份
		body  string // 指定原响应正文
		valid bool   // 是否为合法空页
	}{
		{"empty", `{"code":0,"data":{"items":[],"next_after_id":null}}`, true},
		{"no_code", `{"data":{"items":[],"next_after_id":null}}`, false},
		{"business", `{"code":5003,"data":{"items":[],"next_after_id":null}}`, false},
		{"null_data", `{"code":0,"data":null}`, false},
		{"no_cursor", `{"code":0,"data":{"items":[]}}`, false},
		{"null_items", `{"code":0,"data":{"items":null,"next_after_id":null}}`, false},
		{"empty_cursor", `{"code":0,"data":{"items":[],"next_after_id":11}}`, false},
		{"old_id", `{"code":0,"data":{"items":[{"id":10,"ts":"2026-10-01T00:00:00Z"}],"next_after_id":null}}`, false},
		{"bad_cursor", `{"code":0,"data":{"items":[{"id":11,"ts":"2026-10-01T00:00:00Z"}],"next_after_id":12}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := clientForServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) })
			data, err := client.OrderProcess(context.Background(), contract.OrderProcessRequest{OrderID: 1, OrderNo: "CH", DeviceID: "board", PortNo: 1, AfterID: 10, Limit: 2})
			if tc.valid {
				if err != nil || data.Items == nil || data.NextAfterID != nil {
					t.Fatal(data, err)
				}
			} else if err == nil {
				t.Fatal("invalid response became successful page")
			}
		})
	}
	forwarded := false
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { forwarded = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "query-token")
	if err != nil {
		t.Fatal(err)
	}
	q := contract.OrderProcessRequest{OrderID: 1, OrderNo: "CH", DeviceID: "board", PortNo: 1, Limit: 1}
	if _, err := client.OrderProcess(context.Background(), q); err == nil || forwarded {
		t.Fatal("redirect forwarded service token")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.OrderProcess(ctx, q); !errors.Is(err, context.Canceled) {
		t.Fatal("query ignored cancellation", err)
	}
}
