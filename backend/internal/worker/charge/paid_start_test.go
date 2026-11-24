package charge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	centralcontract "wattharbor/internal/contracts/central"
	gatewaycontract "wattharbor/internal/contracts/gateway"
	centralclient "wattharbor/internal/integration/central"
	gatewayclient "wattharbor/internal/integration/gateway"
	"wattharbor/internal/platform/serviceclient"
)

// starterForServer 为同一本地 HTTP 服务创建中央和网关客户端，并返回 PaidStarter。
func starterForServer(t *testing.T, server *httptest.Server) PaidStarter {
	t.Helper()
	transport := serviceclient.Client{HTTP: server.Client()}
	central, err := centralclient.NewClient(transport, server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := gatewayclient.NewClient(transport, server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	return PaidStarter{Orders: central, Gateway: gateway}
}

func TestPaidStarterRetriesAfterFullScanAndKeepsCursorOnListFailure(t *testing.T) {
	var cursors []string
	listCalls, startCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "secret" {
			t.Error("missing service authentication")
		}
		switch r.URL.Path {
		case "/api/v1/internal/charge-orders/paid-starts":
			if r.Method != "GET" {
				t.Error("list must use GET")
			}
			cursors = append(cursors, r.URL.Query().Get("after_id"))
			listCalls++
			switch listCalls {
			case 1, 4:
				fmt.Fprint(w, `{"code":0,"data":{"items":[{"id":7,"order_no":"C-7"}]}}`)
			case 2:
				w.WriteHeader(503)
			case 3:
				fmt.Fprint(w, `{"code":0,"data":{"items":[]}}`)
			}
		case "/api/v1/internal/charge-orders/start":
			if r.Method != "POST" {
				t.Error("start must use POST")
			}
			var body struct {
				OrderNo string `json:"order_no"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.OrderNo != "C-7" {
				t.Errorf("retry changed original order: %+v %v", body, err)
			}
			startCalls++
			if startCalls == 1 {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(202)
			fmt.Fprint(w, `{"code":0,"data":{"order_no":"C-7","command_id":"cmd-7","status":"pending","device_acknowledged":false}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	s := starterForServer(t, server)
	for i, want := range []struct {
		count int
		fail  bool
	}{{0, true}, {0, true}, {0, false}, {1, false}} {
		got, err := s.DispatchBatch(context.Background())
		if got != want.count || (err != nil) != want.fail {
			t.Fatalf("batch %d: count=%d err=%v", i, got, err)
		}
	}
	if !reflect.DeepEqual(cursors, []string{"0", "7", "7", "0"}) || startCalls != 2 {
		t.Fatalf("cursors=%v starts=%d", cursors, startCalls)
	}
}

func TestPaidStarterRejectsMalformedListBeforeDispatch(t *testing.T) {
	for _, reply := range []string{
		`{"data":{"items":[]}}`,
		`{"code":0,"data":{"items":null}}`,
		`{"code":0,"data":{"items":[{"id":5,"order_no":"C-5"}]}}`,
		`{"code":0,"data":{"items":[{"id":7,"order_no":""}]}}`,
		`{"code":0,"data":{"items":[{"id":8,"order_no":"C-8"},{"id":7,"order_no":"C-7"}]}}`,
	} {
		starts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				starts++
				w.WriteHeader(202)
				return
			}
			fmt.Fprint(w, reply)
		}))
		s := starterForServer(t, server)
		s.cursor = 5
		n, err := s.DispatchBatch(context.Background())
		server.Close()
		if err == nil || n != 0 || starts != 0 || s.cursor != 5 {
			t.Fatalf("reply=%s count=%d starts=%d cursor=%d err=%v", reply, n, starts, s.cursor, err)
		}
	}
}

// pendingStub 返回预设的待启动订单页。
type pendingStub struct {
	// items 固定有序待启动页，测试替身原样返回，不模拟真实查询。
	items []centralcontract.PaidStartOrder
}

// PaidStarts 返回预设订单，不按游标筛选。
func (s pendingStub) PaidStarts(context.Context, uint64) ([]centralcontract.PaidStartOrder, error) {
	return s.items, nil
}

// dispatchStub 记录启动请求，对 C-7 返回预设拒绝。
type dispatchStub struct {
	orders []string
	// rejection 仅对 C-7 返回的 fixture 错误，不表示真实网关状态。
	rejection error
}

// RequestStart 记录订单号；C-7 返回预设拒绝，其他订单返回受理回复，不发送设备命令。
func (s *dispatchStub) RequestStart(_ context.Context, request gatewaycontract.StartRequest) (gatewaycontract.StartReply, error) {
	s.orders = append(s.orders, request.OrderNo)
	if request.OrderNo == "C-7" {
		return gatewaycontract.StartReply{}, s.rejection
	}
	return gatewaycontract.StartReply{OrderNo: request.OrderNo, CommandID: "cmd-8", Status: "pending"}, nil
}

func TestPaidStarterContinuesBatchAfterFailedOrder(t *testing.T) {
	rejection := errors.New("gateway unavailable")
	gateway := &dispatchStub{rejection: rejection}
	s := PaidStarter{Orders: pendingStub{items: []centralcontract.PaidStartOrder{{ID: 7, OrderNo: "C-7"}, {ID: 8, OrderNo: "C-8"}}}, Gateway: gateway}
	n, err := s.DispatchBatch(context.Background())
	if n != 1 || !errors.Is(err, rejection) || s.cursor != 8 || !reflect.DeepEqual(gateway.orders, []string{"C-7", "C-8"}) {
		t.Fatalf("accepted=%d cursor=%d orders=%v err=%v", n, s.cursor, gateway.orders, err)
	}
}
