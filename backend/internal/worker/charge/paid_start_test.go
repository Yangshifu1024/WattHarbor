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

	centralcontract "chargepilot/internal/contracts/central"
	gatewaycontract "chargepilot/internal/contracts/gateway"
	centralclient "chargepilot/internal/integration/central"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/platform/serviceclient"
)

// starterForServer 接收 t与 server，将同一本地HTTP服务注入两真实具名客户端并返回新的 PaidStarter；配置失败终止测试，无数据库或设备 IO。
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

// TestPaidStarterRetriesAfterFullScanAndKeepsCursorOnListFailure 使用 t与实际具名客户端、本地HTTP失败页及受理回复，验证同实例游标保持／空页归零重扫及原订单重投；不证明真库幂等。
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
				// OrderNo 原START请求订单号，只用于本地HTTP夹具核对重投身份。
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
		// count 该轮期望客户端受理条数，不代表设备已启动。
		count int
		// fail 该轮期望返回错误是否非nil，保留原失败序列。
		fail bool
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

// TestPaidStarterRejectsMalformedListBeforeDispatch 使用 t验证原非法页拒绝先于START请求且不推进本实例游标，所有回复值保持；无真实订单或设备写入。
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

// pendingStub 是单批已支付页的窄fixture，只返回原配置记录，无SQL或网络。
type pendingStub struct {
	// items 固定有序待启动页，窄fixture原样返回，不模拟真实查询。
	items []centralcontract.PaidStartOrder
}

// PaidStarts 接收但不使用ctx和afterID，返回配置的原items及nil错误；仅供单批继续行为的窄fixture，不读取数据库。
func (s pendingStub) PaidStarts(context.Context, uint64) ([]centralcontract.PaidStartOrder, error) {
	return s.items, nil
}

// dispatchStub 是记录START尝试并按原订单注入一次拒绝的窄fixture，不实现真实受理或持久幂等。
type dispatchStub struct {
	// orders fixture记录已尝试的原订单号，用于核对调用顺序。
	orders []string
	// rejection 仅对C-7返回的原fixture错误，不表示真实网关状态。
	rejection error
}

// RequestStart 接收但不使用ctx，记录 request.OrderNo；C-7返回配置拒绝，其余返回原受理fixture，无HTTP、数据库或设备 IO。
func (s *dispatchStub) RequestStart(_ context.Context, request gatewaycontract.StartRequest) (gatewaycontract.StartReply, error) {
	s.orders = append(s.orders, request.OrderNo)
	if request.OrderNo == "C-7" {
		return gatewaycontract.StartReply{}, s.rejection
	}
	return gatewaycontract.StartReply{OrderNo: request.OrderNo, CommandID: "cmd-8", Status: "pending"}, nil
}

// TestPaidStarterContinuesBatchAfterFailedOrder 使用 t与窄fixture验证原单单失败后继续本页、受理数、首错误及游标，不能据此推断网关持久幂等。
func TestPaidStarterContinuesBatchAfterFailedOrder(t *testing.T) {
	rejection := errors.New("gateway unavailable")
	gateway := &dispatchStub{rejection: rejection}
	s := PaidStarter{Orders: pendingStub{items: []centralcontract.PaidStartOrder{{ID: 7, OrderNo: "C-7"}, {ID: 8, OrderNo: "C-8"}}}, Gateway: gateway}
	n, err := s.DispatchBatch(context.Background())
	if n != 1 || !errors.Is(err, rejection) || s.cursor != 8 || !reflect.DeepEqual(gateway.orders, []string{"C-7", "C-8"}) {
		t.Fatalf("accepted=%d cursor=%d orders=%v err=%v", n, s.cursor, gateway.orders, err)
	}
}
