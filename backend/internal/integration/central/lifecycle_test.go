package central

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/domain/charging"
	"chargepilot/internal/platform/serviceclient"
)

// TestAuthorizationWireIdentityAndNullableAbsence 验证授权路径、服务令牌、大整数 ID 和设备参数，以及可为空的响应字段。
// 使用本地 HTTP 服务，不验证实际授权、端口占用或设备启动。
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

// TestAuthorizationRejectsConflictAndWrongIdentity 验证 404、409、401、编号不匹配和缺字段均返回对应错误。
func TestAuthorizationRejectsConflictAndWrongIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string

		status int

		body string

		want error
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

// TestResultDeliveryPreservesIdentifiersMeterAndReplay 验证回执中的编号、UTC 毫秒时间、Wh、秒、分段计量和重复提交标志保持一致。
// 使用本地 HTTP 服务，不验证实际事务提交。
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

// TestResultDeliveryRequiresPersistenceAcceptance 验证缺字段、未接受、HTTP 202、冲突和无效 JSON 响应均被拒绝。
func TestResultDeliveryRequiresPersistenceAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name string

		status int

		body string

		want error
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
