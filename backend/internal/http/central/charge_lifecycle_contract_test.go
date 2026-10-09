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

// lifecycleAuthorizations 返回固定起充／停机结果，记录客户端停止授权传入的用户身份。
type lifecycleAuthorizations struct {
	userID uint64 // 最近一次停止授权的用户主键，核对大整数未被截断。
}

// Start 忽略输入 context 和订单号，返回固定起充结果及 nil 错误；不修改 s、执行 IO 或模拟取消。
func (s *lifecycleAuthorizations) Start(context.Context, string) (chargestart.StartAuthorization, error) {
	return chargestart.StartAuthorization{PaidStartAuthorization: chargestart.PaidStartAuthorization{ChargeOrderID: 9, OrderNo: "C-9", UserID: 9007199254740993, DeviceID: "D-1", PortNo: 1, PaymentOrderID: 7, ChargeMode: 1, ChargeQuantity: 1200}, ConsumerType: 2}, nil
}

// Stop 忽略输入 context 和订单号，以 id 记录 s.userID 并返回携身份／启动命令的固定停机结果和 nil 错误。
// 仅修改 fixture 记录，不冻结实际截止点、访问数据库或模拟取消。
func (s *lifecycleAuthorizations) Stop(_ context.Context, _ string, id uint64) (chargestop.ActiveStopAuthorization, error) {
	s.userID = id
	return chargestop.ActiveStopAuthorization{ChargeOrderID: 9, OrderNo: "C-9", UserID: id, DeviceID: "D-1", PortNo: 1, PortID: sql.NullInt64{Int64: 9007199254740994, Valid: true}, StartCommandID: receiptStartID}, nil
}

// lifecycleStartResults 捕获真实起充回执 HTTP 适配传入的领域参数，不执行持久事务。
type lifecycleStartResults struct {
	result chargestart.StartResult // 最近一次传入的起充回执，核对命令及端口编号。
	calls  int                     // Apply 调用次数，核对坏路径订单号未进入用例。
}

// Apply 忽略输入 context，以 r 覆盖 s.result 并递增次数，固定返回重放 true 和 nil 错误。
// 仅记录 fixture 参数，不保存实际状态或模拟事务、取消及幂等竞争。
func (s *lifecycleStartResults) Apply(_ context.Context, r chargestart.StartResult) (bool, error) {
	s.calls++
	s.result = r
	return true, nil
}

// lifecycleEndResults 捕获真实结束回执 HTTP 适配传入的领域参数，不执行持久事务。
type lifecycleEndResults struct {
	result chargefinish.EndResult // 最近一次传入的结束回执，核对计量单位及停止命令。
	calls  int                    // Apply 调用次数，核对坏路径订单号未进入用例。
}

// Apply 忽略输入 context，以 r 覆盖 s.result 并递增次数，固定返回重放 true 和 nil 错误。
// 仅记录 fixture 参数，不保存结束状态、核算资金或模拟取消、事务和幂等竞争。
func (s *lifecycleEndResults) Apply(_ context.Context, r chargefinish.EndResult) (bool, error) {
	s.calls++
	s.result = r
	return true, nil
}

// TestLifecycleClientMatchesRegisteredCentralRoutes 核对专用客户端与四个真实注册路由的双向契约。
// 覆盖大整数编号、回执字段与计量单位、重放形状，以及路径订单号失配时拒绝调用；
// 用例均为参数 fixture，不验证数据库事务、真实幂等或设备执行，相关证据属于 integration 用例测试。
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
		path    string // 与 JSON 订单号故意失配的真实回执路由。
		payload any    // 起充或结束请求，保持命令、身份、时刻及计量事实。
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
