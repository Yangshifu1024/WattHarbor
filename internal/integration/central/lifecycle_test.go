package central

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

func TestAuthorizationWireIdentityAndNullableAbsence(t *testing.T) {
	client := centralForServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("X-Service-Token") != "secret" {
			t.Error("invalid authorization request")
		}
		switch r.URL.Path {
		case "/api/v1/internal/charge-orders/C-1/start-authorization":
			fmt.Fprint(w, `{"code":0,"data":{"charge_order_id":9007199254740993,"payment_order_id":7,"order_no":"C-1","user_id":9007199254740994,"device_id":"D-1","port_no":2,"charge_mode":1,"charge_quantity":1200,"consumer_type":3,"card_number":123,"card_balance_units":45}}`)
		case "/api/v1/internal/charge-orders/C-1/stop-authorization":
			if r.URL.Query().Get("user_id") != "9007199254740994" {
				t.Error("identity lost")
			}
			fmt.Fprint(w, `{"code":0,"data":{"charge_order_id":9007199254740993,"order_no":"C-1","user_id":9007199254740994,"device_id":"D-1","port_no":2,"port_id":9007199254740995,"start_command_id":"start-1"}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	})
	start, err := client.StartAuthorization(context.Background(), "C-1")
	if err != nil || start.ChargeOrderID != 9007199254740993 || start.PaymentOrderID != 7 || start.ChargeQuantity != 1200 || start.CardBalanceUnits != 45 || start.ConsumerType != 3 || start.CardNumber != 123 {
		t.Fatalf("start=%+v err=%v", start, err)
	}
	stop, err := client.StopAuthorization(context.Background(), "C-1", 9007199254740994)
	if err != nil || stop.PortID != 9007199254740995 || stop.UserID != 9007199254740994 || stop.StartCommandID != "start-1" {
		t.Fatalf("stop=%+v err=%v", stop, err)
	}
}

func TestAuthorizationRejectsConflictAndWrongIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"not found", 404, `{}`, centralcontract.ErrAuthorizationDenied},
		{"conflict", 409, `{}`, centralcontract.ErrAuthorizationDenied},
		{"wrong order", 200, `{"code":0,"data":{"charge_order_id":1,"order_no":"other","user_id":42}}`, centralcontract.ErrAuthorizationDenied},
		{"missing identity", 200, `{"code":0,"data":{"order_no":"C-1","user_id":42}}`, centralcontract.ErrAuthorizationDenied},
		{"missing code", 200, `{"data":{"charge_order_id":1,"order_no":"C-1","user_id":42}}`, ErrInvalidResponse},
		{"missing data", 200, `{"code":0}`, ErrInvalidResponse},
		{"unauthorized", 401, `{}`, serviceclient.ErrUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := centralForServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) })
			_, err := client.StartAuthorization(context.Background(), "C-1")
			if !errors.Is(err, tc.want) {
				t.Fatalf("start err=%v want=%v", err, tc.want)
			}
			_, err = client.StopAuthorization(context.Background(), "C-1", 42)
			if !errors.Is(err, tc.want) {
				t.Fatalf("stop err=%v want=%v", err, tc.want)
			}
		})
	}
	client := centralForServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"charge_order_id":1,"order_no":"C-1","user_id":41}}`)
	})
	if _, err := client.StopAuthorization(context.Background(), "C-1", 42); !errors.Is(err, centralcontract.ErrAuthorizationDenied) {
		t.Fatalf("wrong owner accepted: %v", err)
	}
}

func TestResultDeliveryPreservesIdentifiersMeterAndReplay(t *testing.T) {
	stamp := time.Date(2026, 10, 5, 4, 0, 0, 123000000, time.UTC)
	client := centralForServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("X-Service-Token") != "secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("invalid receipt request")
		}
		var data map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			t.Error(err)
		}
		if string(data["charge_order_id"]) != "9007199254740993" || string(data["port_id"]) != "9007199254740994" || string(data["order_no"]) != `"C-1"` {
			t.Errorf("identity changed: %v", data)
		}
		switch r.URL.Path {
		case "/api/v1/internal/charge-orders/C-1/start-result":
			if string(data["command_id"]) != `"start-1"` || string(data["success"]) != "true" || string(data["result_code"]) != "0" || string(data["occurred_at"]) != `"2026-10-05T04:00:00.123Z"` {
				t.Error("start receipt changed")
			}
		case "/api/v1/internal/charge-orders/C-1/end-result":
			var meter charging.EndMeter
			if json.Unmarshal(data["meter"], &meter) != nil || meter.ChargedWh != 1000 || meter.ChargedSeconds != 3600 || !meter.EndedAt.Equal(stamp) || len(meter.Segments) != 1 || meter.Segments[0].EnergyWh != 1000 {
				t.Errorf("meter or units changed: %+v", meter)
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"code":0,"data":{"accepted":true,"replayed":true}}`)
	})
	start := centralcontract.StartResultRequest{OrderNo: "C-1", ChargeOrderID: 9007199254740993, PortID: 9007199254740994, CommandID: "start-1", Success: true, OccurredAt: stamp}
	end := centralcontract.EndResultRequest{OrderNo: "C-1", ChargeOrderID: start.ChargeOrderID, PortID: start.PortID, Meter: charging.EndMeter{ChargedWh: 1000, ChargedSeconds: 3600, EndedAt: stamp, Segments: []charging.MeterSegment{{EnergyWh: 1000}}}}
	a, err := client.SubmitStartResult(context.Background(), start)
	if err != nil || !a.Accepted || !a.Replayed {
		t.Fatalf("start ack=%+v err=%v", a, err)
	}
	a, err = client.SubmitEndResult(context.Background(), end)
	if err != nil || !a.Accepted || !a.Replayed {
		t.Fatalf("end ack=%+v err=%v", a, err)
	}
}

func TestResultDeliveryRequiresPersistenceAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"missing code", 200, `{"data":{"accepted":true}}`, ErrInvalidResponse},
		{"missing data", 200, `{"code":0}`, ErrInvalidResponse},
		{"not accepted", 200, `{"code":0,"data":{"accepted":false}}`, ErrInvalidResponse},
		{"202 is not persistence", 202, `{"code":0,"data":{"accepted":true}}`, ErrInvalidResponse},
		{"conflict", 409, `{}`, centralcontract.ErrResultConflict},
		{"malformed", 200, `{`, ErrInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := centralForServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) })
			_, err := client.SubmitStartResult(context.Background(), centralcontract.StartResultRequest{OrderNo: "C-1"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("start=%v", err)
			}
			_, err = client.SubmitEndResult(context.Background(), centralcontract.EndResultRequest{OrderNo: "C-1"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("end=%v", err)
			}
		})
	}
}
