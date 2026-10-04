package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	chargefinish "github.com/ChargePilot2026/charge-pilot/internal/charging/finish"
	chargestart "github.com/ChargePilot2026/charge-pilot/internal/charging/start"
	chargestop "github.com/ChargePilot2026/charge-pilot/internal/charging/stop"
	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	centralclient "github.com/ChargePilot2026/charge-pilot/internal/integration/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

const receiptStartID = "10000000-0000-4000-8000-000000000001"
const receiptStopID = "10000000-0000-4000-8000-000000000002"

type lifecycleAuthorizations struct{ userID uint64 }

func (s *lifecycleAuthorizations) Start(context.Context, string) (chargestart.StartAuthorization, error) {
	return chargestart.StartAuthorization{PaidStartAuthorization: chargestart.PaidStartAuthorization{ChargeOrderID: 9, OrderNo: "C-9", UserID: 9007199254740993, DeviceID: "D-1", PortNo: 1, PaymentOrderID: 7, ChargeMode: 1, ChargeQuantity: 1200}, ConsumerType: 2}, nil
}
func (s *lifecycleAuthorizations) Stop(_ context.Context, _ string, id uint64) (chargestop.ActiveStopAuthorization, error) {
	s.userID = id
	return chargestop.ActiveStopAuthorization{ChargeOrderID: 9, OrderNo: "C-9", UserID: id, DeviceID: "D-1", PortNo: 1, PortID: sql.NullInt64{Int64: 9007199254740994, Valid: true}, StartCommandID: receiptStartID}, nil
}

type lifecycleStartResults struct {
	result chargestart.StartResult
	calls  int
}

func (s *lifecycleStartResults) Apply(_ context.Context, r chargestart.StartResult) (bool, error) {
	s.calls++
	s.result = r
	return true, nil
}

type lifecycleEndResults struct {
	result chargefinish.EndResult
	calls  int
}

func (s *lifecycleEndResults) Apply(_ context.Context, r chargefinish.EndResult) (bool, error) {
	s.calls++
	s.result = r
	return true, nil
}

// 替换用例依赖，验证实际绑定与响应；事务语义由带 integration tag 的用例测试覆盖。
func TestLifecycleClientMatchesRegisteredCentralRoutes(t *testing.T) {
	authorizations := &lifecycleAuthorizations{}
	starts := &lifecycleStartResults{}
	ends := &lifecycleEndResults{}
	router := httpapi.NewRouter()
	StartAuthorization{Service: authorizations, ServiceToken: "secret"}.Register(router)
	StopAuthorization{Service: authorizations, ServiceToken: "secret"}.Register(router)
	StartResultAPI{Service: starts, ServiceToken: "secret"}.Register(router)
	EndResultAPI{Service: ends, ServiceToken: "secret"}.Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := centralclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	paid, err := client.StartAuthorization(context.Background(), "C-9")
	if err != nil || paid.UserID != 9007199254740993 || paid.ChargeQuantity != 1200 || paid.PaymentOrderID != 7 || paid.CardBalanceUnits != 0 {
		t.Fatalf("paid=%+v err=%v", paid, err)
	}
	active, err := client.StopAuthorization(context.Background(), "C-9", paid.UserID)
	if err != nil || active.PortID != 9007199254740994 || authorizations.userID != paid.UserID {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	stamp := time.Date(2026, 10, 5, 4, 0, 0, 123000000, time.UTC)
	start := centralcontract.StartResult{CommandID: receiptStartID, ChargeOrderID: 9, OrderNo: "C-9", DeviceID: "D-1", PortNo: 1, PortID: active.PortID, Success: true, OccurredAt: stamp}
	ack, err := client.SubmitStartResult(context.Background(), start)
	if err != nil || !ack.Accepted || !ack.Replayed || starts.result.CommandID != receiptStartID || starts.result.PortID != active.PortID {
		t.Fatalf("start ack=%+v result=%+v err=%v", ack, starts.result, err)
	}
	end := centralcontract.EndResult{OrderNo: "C-9", ChargeOrderID: 9, StartCommandID: receiptStartID, StopCommandID: receiptStopID, DeviceID: "D-1", PortNo: 1, PortID: active.PortID, Meter: charging.EndMeter{ChargedWh: 1000, ChargedSeconds: 3600, EndedAt: stamp}}
	ack, err = client.SubmitEndResult(context.Background(), end)
	if err != nil || !ack.Accepted || !ack.Replayed || ends.result.Meter.ChargedWh != 1000 || ends.result.Meter.ChargedSeconds != 3600 || ends.result.StopCommandID != receiptStopID {
		t.Fatalf("end ack=%+v result=%+v err=%v", ack, ends.result, err)
	}
	for _, tc := range []struct {
		path    string
		payload any
	}{
		{"/api/v1/internal/charge-orders/other/start-result", start},
		{"/api/v1/internal/charge-orders/other/end-result", end},
	} {
		data, _ := json.Marshal(tc.payload)
		r := httptest.NewRequest("POST", tc.path, strings.NewReader(string(data)))
		r.Header.Set("X-Service-Token", "secret")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || starts.calls != 1 || ends.calls != 1 {
			t.Fatalf("mismatched order reached usecase: status=%d", w.Code)
		}
	}
}
