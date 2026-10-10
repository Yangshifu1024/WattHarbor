package control

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/domain/devices"
	"chargepilot/internal/gateway/repo"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
)

// startContractService 记录启动调用，返回预设状态和错误。
type startContractService struct {
	// orderNo、status 测试替身观察的订单号。 测试预期或替身返回的 HTTP 状态。
	orderNo string
	status  devices.ChargeCommandStatus
	calls   int
	err     error
}

// Start 记录订单号和调用次数，返回预设启动结果。
func (s *startContractService) Start(_ context.Context, orderNo string) (repo.StartReservation, error) {
	s.orderNo, s.calls = orderNo, s.calls+1
	return repo.StartReservation{OrderNo: orderNo, CommandID: "cmd-1", Status: s.status}, s.err
}

// 注册真实路由并替换用例依赖，验证 HTTP 契约；此测试不验证持久化或设备通信。
func TestStartClientMatchesRegisteredGatewayRoute(t *testing.T) {
	for _, status := range []devices.ChargeCommandStatus{devices.CommandPending, devices.CommandSent, devices.CommandStopping, devices.CommandAcked} {
		t.Run(string(status), func(t *testing.T) {
			service := &startContractService{status: status}
			router := httpapi.NewRouter()
			StartAPI{Service: service, ServiceToken: "secret"}.Register(router)
			server := httptest.NewServer(router)
			defer server.Close()
			client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "secret")
			if err != nil {
				t.Fatal(err)
			}
			data, err := client.RequestStart(context.Background(), gatewaycontract.StartRequest{OrderNo: "C-1"})
			if err != nil || data.OrderNo != "C-1" || data.CommandID != "cmd-1" || data.Status != status || data.DeviceAcknowledged != (status == "acked") || service.orderNo != "C-1" || service.calls != 1 {
				t.Fatalf("acceptance=%+v service=%+v err=%v", data, service, err)
			}
		})
	}
}

func TestStartRoutePreservesInputAuthorizationAndConflict(t *testing.T) {
	for _, tc := range []struct {
		// name、token、body 测试场景名称。 测试服务令牌。 测试 HTTP 请求正文。
		name, token, body string
		err               error
		// status、calls 测试预期或替身返回的 HTTP 状态。 测试替身记录的调用次数，不产生外部 IO。
		status, calls int
	}{
		{"service token", "", `{"order_no":"C-1"}`, nil, 401, 0},
		{"empty order", "secret", `{"order_no":""}`, nil, 400, 0},
		{"leading whitespace", "secret", `{"order_no":" C-1"}`, nil, 400, 0},
		{"long order", "secret", `{"order_no":"` + strings.Repeat("C", 65) + `"}`, nil, 400, 0},
		{"unpaid", "secret", `{"order_no":"C-1"}`, ErrUnpaid, 409, 1},
		{"port occupied", "secret", `{"order_no":"C-1"}`, repo.ErrPortUnavailable, 409, 1},
		{"unavailable", "secret", `{"order_no":"C-1"}`, errors.New("private failure"), 503, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &startContractService{err: tc.err}
			router := httpapi.NewRouter()
			StartAPI{Service: service, ServiceToken: "secret"}.Register(router)
			request := httptest.NewRequest(http.MethodPost, gatewaycontract.StartPath, strings.NewReader(tc.body))
			request.Header.Set("X-Service-Token", tc.token)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status || service.calls != tc.calls || strings.Contains(response.Body.String(), "private failure") {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, service.calls, response.Body.String())
			}
		})
	}
}
