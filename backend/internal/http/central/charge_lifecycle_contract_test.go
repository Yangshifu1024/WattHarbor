package central

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	chargefinish "chargepilot/internal/charging/finish"
	chargestart "chargepilot/internal/charging/start"
	chargestop "chargepilot/internal/charging/stop"
	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/domain/charging"
	centralclient "chargepilot/internal/integration/central"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
)

const receiptStartID = "10000000-0000-4000-8000-000000000001"
const receiptStopID = "10000000-0000-4000-8000-000000000002"

// lifecycleAuthorizations 返回固定启动和停止授权，记录停止请求的用户 ID。
type lifecycleAuthorizations struct {
	userID uint64 // 最近一次停止授权的用户主键，核对大整数未被截断。
}

// Start 返回固定启动授权。
func (s *lifecycleAuthorizations) Start(context.Context, string) (chargestart.StartAuthorization, error) {
	return chargestart.StartAuthorization{PaidStartAuthorization: chargestart.PaidStartAuthorization{ChargeOrderID: 9, OrderNo: "C-9", UserID: 9007199254740993, DeviceID: "D-1", PortNo: 1, PaymentOrderID: 7, ChargeMode: 1, ChargeQuantity: 1200}, ConsumerType: 2}, nil
}

// Stop 记录用户 ID，返回固定停止授权。
func (s *lifecycleAuthorizations) Stop(_ context.Context, _ string, id uint64) (chargestop.ActiveStopAuthorization, error) {
	s.userID = id
	return chargestop.ActiveStopAuthorization{ChargeOrderID: 9, OrderNo: "C-9", UserID: id, DeviceID: "D-1", PortNo: 1, PortID: sql.NullInt64{Int64: 9007199254740994, Valid: true}, StartCommandID: receiptStartID}, nil
}

// lifecycleStartResults 记录 HTTP 处理器提交的启动结果。
type lifecycleStartResults struct {
	result chargestart.StartResult // 最近一次传入的起充回执，核对命令及端口编号。
	calls  int                     // Apply 调用次数，核对坏路径订单号未进入用例。
}

// Apply 记录提交结果和调用次数，返回 replayed=true。
func (s *lifecycleStartResults) Apply(_ context.Context, r chargestart.StartResult) (bool, error) {
	s.calls++
	s.result = r
	return true, nil
}

// lifecycleEndResults 记录 HTTP 处理器提交的结束结果。
type lifecycleEndResults struct {
	result chargefinish.EndResult // 最近一次传入的结束回执，核对计量单位及停止命令。
	calls  int                    // Apply 调用次数，核对坏路径订单号未进入用例。
}

// Apply 记录提交结果和调用次数，返回 replayed=true。
func (s *lifecycleEndResults) Apply(_ context.Context, r chargefinish.EndResult) (bool, error) {
	s.calls++
	s.result = r
	return true, nil
}

// TestLifecycleClientMatchesRegisteredCentralRoutes 核对客户端和四个注册接口的请求及回复。
// 业务服务由测试替身记录参数；检查大整数编号、计量单位、重放字段，以及路径订单号与正文不一致时拒绝调用。
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
	start := centralcontract.StartResultRequest{CommandID: receiptStartID, ChargeOrderID: 9, OrderNo: "C-9", DeviceID: "D-1", PortNo: 1, PortID: active.PortID, Success: true, OccurredAt: stamp}
	ack, err := client.SubmitStartResult(context.Background(), start)
	if err != nil || !ack.Accepted || !ack.Replayed || starts.result.CommandID != receiptStartID || starts.result.PortID != active.PortID {
		t.Fatalf("start ack=%+v result=%+v err=%v", ack, starts.result, err)
	}
	end := centralcontract.EndResultRequest{OrderNo: "C-9", ChargeOrderID: 9, StartCommandID: receiptStartID, StopCommandID: receiptStopID, DeviceID: "D-1", PortNo: 1, PortID: active.PortID, Meter: charging.EndMeter{ChargedWh: 1000, ChargedSeconds: 3600, EndedAt: stamp}}
	ack, err = client.SubmitEndResult(context.Background(), end)
	if err != nil || !ack.Accepted || !ack.Replayed || ends.result.Meter.ChargedWh != 1000 || ends.result.Meter.ChargedSeconds != 3600 || ends.result.StopCommandID != receiptStopID {
		t.Fatalf("end ack=%+v result=%+v err=%v", ack, ends.result, err)
	}
	for _, tc := range []struct {
		path    string // 路径订单号故意与 JSON 不同。
		payload any    // 启动或结束请求，包含原命令编号、订单编号、时刻和计量读数。
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
