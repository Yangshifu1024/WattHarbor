package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
)

func TestSyncReadOperationsPreserveNullsAndEvidenceWindow(t *testing.T) {
	stamp := time.Date(2026, 10, 5, 12, 0, 0, 123000000, time.FixedZone("CST", 8*3600))
	client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("X-Service-Token") != "test-service-token" {
			t.Error("invalid sync request")
		}
		switch r.URL.Path {
		case "/api/v1/internal/start-results":
			if r.URL.Query().Get("command_id") != "cmd&1" {
				t.Error("filter not escaped")
			}
			fmt.Fprint(w, `{"code":0,"data":{"items":[{"command_id":"cmd-1","port_id":null,"result_code":null,"ack_at":null}]}}`)
		case "/api/v1/internal/end-events":
			fmt.Fprint(w, `{"code":0,"data":{"items":[{"id":9007199254740993,"payload":"{}"}]}}`)
		case "/api/v1/internal/charge-commands/by-order/9":
			fmt.Fprint(w, `{"code":0,"data":{"found":true,"command":{"charge_order_id":9,"command_id":"cmd-1","port_id":9007199254740993,"result_code":0,"ack_at":"2026-10-05T04:00:00.123Z"}}}`)
		case "/api/v1/internal/charge-commands/by-order/10":
			fmt.Fprint(w, `{"code":0,"data":{"found":false}}`)
		case "/api/v1/internal/devices/D-1/meter-samples":
			if r.URL.Query().Get("ack_at") != "2026-10-05T04:00:00.123Z" || r.URL.Query().Get("end_at") != "2026-10-05T05:00:00.123Z" || r.URL.Query().Get("end_id") != "9007199254740993" {
				t.Error("evidence window changed")
			}
			fmt.Fprint(w, `{"code":0,"data":{"items":[]}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	rows, err := client.StartResults(context.Background(), "cmd&1")
	if err != nil || len(rows) != 1 || rows[0].PortID != nil || rows[0].ResultCode != nil || rows[0].AckAt != nil {
		t.Fatalf("null evidence changed: %+v %v", rows, err)
	}
	events, err := client.EndEvents(context.Background())
	if err != nil || len(events) != 1 || events[0].ID != 9007199254740993 || events[0].Payload != "{}" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	command, err := client.ChargeCommand(context.Background(), 9)
	if err != nil || command == nil || command.PortID == nil || *command.PortID != 9007199254740993 || !command.AckAt.Equal(stamp) {
		t.Fatalf("command=%+v err=%v", command, err)
	}
	command, err = client.ChargeCommand(context.Background(), 10)
	if err != nil || command != nil {
		t.Fatalf("missing command=%+v err=%v", command, err)
	}
	samples, err := client.MeterSamples(context.Background(), gatewaycontract.MeterSamplesQuery{DeviceID: "D-1", AckAt: stamp, EndAt: stamp.Add(time.Hour), EndID: 9007199254740993})
	if err != nil || samples == nil || len(samples) != 0 {
		t.Fatalf("samples=%+v err=%v", samples, err)
	}
}

func TestSyncMutationsRequirePositiveAcceptanceAndAllowReplay(t *testing.T) {
	client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("X-Service-Token") != "test-service-token" {
			t.Error("invalid mutation")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/api/v1/internal/start-results/mark-reported":
			if string(body["command_ids"]) != `["cmd-1"]` {
				t.Error("command identity changed")
			}
			fmt.Fprint(w, `{"code":0,"data":{"marked":0}}`)
		case "/api/v1/internal/device-events/mark-processed":
			if string(body["ids"]) != `[9007199254740993]` {
				t.Error("event identity changed")
			}
			fmt.Fprint(w, `{"code":0,"data":{"marked":0}}`)
		case "/api/v1/internal/ports/release":
			if string(body["port_id"]) != "9007199254740993" || string(body["order_no"]) != `"C-1"` {
				t.Error("release identity changed")
			}
			fmt.Fprint(w, `{"code":0,"data":{"released":true,"already":true}}`)
		case "/api/v1/internal/charge-end-deliveries/freeze":
			if string(body["device_event_id"]) != "9007199254740993" || string(body["charge_order_id"]) != "9" {
				t.Error("freeze identity changed")
			}
			fmt.Fprint(w, `{"code":0,"data":{"payload":"stored-payload","frozen":true}}`)
		case "/api/v1/internal/charge-orders/C-1/compensate":
			if string(body["command_id"]) != `"cmd-1"` {
				t.Error("compensation identity changed")
			}
			fmt.Fprint(w, `{"code":0,"data":{"accepted":true}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	if n, err := client.MarkStartResultsReported(context.Background(), []string{"cmd-1"}); err != nil || n != 0 {
		t.Fatalf("mark=%d err=%v", n, err)
	}
	if n, err := client.MarkDeviceEventsProcessed(context.Background(), []uint64{9007199254740993}); err != nil || n != 0 {
		t.Fatalf("mark=%d err=%v", n, err)
	}
	if data, err := client.ReleasePort(context.Background(), gatewaycontract.ReleasePortRequest{PortID: 9007199254740993, OrderNo: "C-1"}); err != nil || !data.Already {
		t.Fatalf("release=%+v err=%v", data, err)
	}
	if data, err := client.FreezeEndDelivery(context.Background(), gatewaycontract.FreezeEndDeliveryRequest{DeviceEventID: 9007199254740993, ChargeOrderID: 9, Payload: "candidate"}); err != nil || data.Payload != "stored-payload" {
		t.Fatalf("freeze=%+v err=%v", data, err)
	}
	if err := client.CompensateStart(context.Background(), "C-1", "cmd-1"); err != nil {
		t.Fatal(err)
	}
}

func TestSyncClientRejectsMissingAcknowledgements(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		call       func(*Client) error
	}{
		{"missing list", `{"code":0,"data":{}}`, func(c *Client) error { _, err := c.EndEvents(context.Background()); return err }},
		{"wrong command", `{"code":0,"data":{"found":true,"command":{"charge_order_id":10}}}`, func(c *Client) error { _, err := c.ChargeCommand(context.Background(), 9); return err }},
		{"missing marked", `{"code":0,"data":{}}`, func(c *Client) error {
			_, err := c.MarkStartResultsReported(context.Background(), []string{"cmd"})
			return err
		}},
		{"wrong marked", `{"code":0,"data":{"marked":2}}`, func(c *Client) error {
			_, err := c.MarkDeviceEventsProcessed(context.Background(), []uint64{1})
			return err
		}},
		{"release false", `{"code":0,"data":{"released":false}}`, func(c *Client) error {
			_, err := c.ReleasePort(context.Background(), gatewaycontract.ReleasePortRequest{})
			return err
		}},
		{"missing freeze", `{"code":0,"data":{}}`, func(c *Client) error {
			_, err := c.FreezeEndDelivery(context.Background(), gatewaycontract.FreezeEndDeliveryRequest{})
			return err
		}},
		{"false compensation", `{"code":0,"data":{"accepted":false}}`, func(c *Client) error { return c.CompensateStart(context.Background(), "C-1", "cmd") }},
		{"missing code", `{"data":{"accepted":true}}`, func(c *Client) error { return c.CompensateStart(context.Background(), "C-1", "cmd") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) })
			if err := tc.call(client); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("invalid acknowledgement accepted: %v", err)
			}
		})
	}
}
