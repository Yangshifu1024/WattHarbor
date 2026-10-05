package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
)

// TestAlertClientsKeepOriginalIDsWatermarkAndReplay 用loopback验证原大整数、水印和零标记重放，不代表真库证据。
func TestAlertClientsKeepOriginalIDsWatermarkAndReplay(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 123000000, time.UTC)
	later := at.Add(time.Second)
	client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "test-service-token" {
			t.Error("token lost")
		}
		switch r.URL.Path {
		case contract.DeviceFaultsPath:
			if r.URL.Query().Get("limit") != "100" {
				t.Error("batch changed")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": contract.DeviceFaultsReply{Reports: []contract.DeviceFaultReport{{ID: 18446744073709551614, DeviceID: "原设备", EventKey: "原键", ReceivedAt: at, Payload: "{}"}}}})
		case contract.MarkDeviceFaultsPath:
			var request contract.MarkDeviceFaultsRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.IDs) != 1 || request.IDs[0] != 18446744073709551614 {
				t.Errorf("identity=%+v %v", request, err)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"marked":0}}`))
		case "/api/v1/internal/devices/原&+设备/latest-heartbeat":
			if r.URL.Query().Get("after_key") != "原&+键" || r.URL.Query().Get("after") != "" {
				t.Error("watermark changed")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": contract.HeartbeatAfterKeyReply{Found: true, AfterReceivedAt: &at, ReceivedAt: &later, Payload: "{}"}})
		default:
			t.Errorf("path=%s", r.URL)
		}
	})
	rows, err := client.DeviceFaults(context.Background())
	if err != nil || len(rows) != 1 || rows[0].ID != 18446744073709551614 {
		t.Fatalf("rows=%+v %v", rows, err)
	}
	n, err := client.MarkDeviceFaults(context.Background(), []uint64{18446744073709551614})
	if err != nil || n != 0 {
		t.Fatalf("replay=%d %v", n, err)
	}
	hb, err := client.HeartbeatAfterKey(context.Background(), "原&+设备", "原&+键")
	if err != nil || hb.AfterReceivedAt == nil || !hb.AfterReceivedAt.Equal(at) {
		t.Fatalf("heartbeat=%+v %v", hb, err)
	}
}

// TestAlertClientsRejectMissingContradictoryEvidence 验证缺字段、倒退时刻、重定向及取消均是错误，缺失原水印可明确表达。
func TestAlertClientsRejectMissingContradictoryEvidence(t *testing.T) {
	for _, body := range []string{`{"code":0,"data":{}}`, `{"code":0,"data":{"found":true}}`, `{"code":0,"data":{"found":false,"payload":"{}"}}`, `{"code":0,"data":{"found":true,"after_received_at":"2026-10-01T00:00:01Z","received_at":"2026-10-01T00:00:00Z","payload":"{}"}}`} {
		client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
		if _, err := client.HeartbeatAfterKey(context.Background(), "board", "key"); !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("body=%s err=%v", body, err)
		}
	}
	client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"found":false}}`))
	})
	data, err := client.HeartbeatAfterKey(context.Background(), "board", "missing")
	if err != nil || data.Found || data.AfterReceivedAt != nil {
		t.Fatalf("missing=%+v %v", data, err)
	}
	redirect := clientForServer(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	if _, err := redirect.DeviceFaults(context.Background()); err == nil {
		t.Fatal("redirect accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.DeviceFaults(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, body := range []string{`{"code":0,"data":{}}`, `{"code":0,"data":{"marked":-1}}`, `{"code":0,"data":{"marked":2}}`} {
		c := clientForServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
		if _, err := c.MarkDeviceFaults(context.Background(), []uint64{1}); !errors.Is(err, ErrInvalidResponse) {
			t.Fatal(body, err)
		}
	}
}
