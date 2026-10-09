package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/platform/serviceclient"
)

// clientForServer 为测试创建本地 HTTP 服务和 gateway 客户端，并注册服务清理。
func clientForServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL+"/", "test-service-token")
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestChargingEvidencePreservesWireFieldsAndCursor 验证请求中的设备、端口、启动时间和游标，以及响应中的单位、完整标志和下一页游标。
// 使用本地 HTTP 服务，不验证实际数据库筛选。
func TestChargingEvidencePreservesWireFieldsAndCursor(t *testing.T) {
	started := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/internal/charging-evidence" || r.Header.Get("X-Service-Token") != "test-service-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: method=%s path=%s", r.Method, r.URL.Path)
		}
		var body struct {
			// Requests 是用于核对的批量查询参数。
			Requests []struct {
				// Device 是请求的设备编号。
				Device string `json:"device_id"`
				// Port 端口序号，从 1 开始，不改协议数值。
				Port uint8 `json:"port_no"`
				// Start 是查询开始时间，用于比较响应。
				Start time.Time `json:"started_at"`
				// After 是 uint64 事件 ID 游标，不经 float64 转换。
				After uint64 `json:"after_id"`
			} `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Requests) != 1 {
			t.Errorf("invalid request body: %+v %v", body, err)
		} else if q := body.Requests[0]; q.Device != "D-1" || q.Port != 2 || !q.Start.Equal(started) || q.After != 41 {
			t.Errorf("unexpected query: %+v", q)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"evidence":[{"device_id":"D-1","port_no":2,"samples":[{"Type":"heartbeat","DeviceID":"D-1","ReceivedAt":"2026-10-04T00:01:00Z","ChargingPorts":[{"Port":2,"ChargedMWh":12000,"ChargedSeconds":60,"PowerDeciWatts":20}]}],"complete":false,"next_after_id":93}]}}`))
	})
	pages, err := client.ChargingEvidence(context.Background(), []gatewaycontract.EvidenceQueryRequest{{DeviceID: "D-1", PortNo: 2, StartedAt: started, AfterID: 41}})
	if err != nil || len(pages) != 1 {
		t.Fatalf("pages=%+v err=%v", pages, err)
	}
	page := pages[0]
	if page.Complete || page.NextAfterID != 93 || len(page.Samples) != 1 || len(page.Samples[0].ChargingPorts) != 1 {
		t.Fatalf("wire fields not decoded: %+v", page)
	}
	port := page.Samples[0].ChargingPorts[0]
	if page.Samples[0].DeviceID != "D-1" || page.Samples[0].ReceivedAt.IsZero() || port.ChargedMWh != 12000 || port.PowerDeciWatts != 20 || port.ChargedSeconds != 60 {
		t.Fatalf("sample or units changed: %+v", page.Samples[0])
	}
}

// TestRequestStopRequiresAcceptedResponse 验证停机请求的大整数 ID 和来源，以及 HTTP 202、stopped=false 的响应。
// 网关接受请求不代表设备已停机。
func TestRequestStopRequiresAcceptedResponse(t *testing.T) {
	client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/charge-orders/stop" || r.Method != http.MethodPost || r.Header.Get("X-Service-Token") != "test-service-token" {
			t.Errorf("unexpected stop request: %s %s", r.Method, r.URL.Path)
		}
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if string(request["order_no"]) != `"C-1"` || string(request["user_id"]) != "9007199254740993" || string(request["source"]) != `"auto"` {
			t.Errorf("unexpected stop payload: %v", request)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"code":0,"data":{"accepted":true,"stopped":false,"command_id":"stop-command","status":"pending"}}`))
	})
	result, err := client.RequestStop(context.Background(), gatewaycontract.StopRequest{OrderNo: "C-1", UserID: 9007199254740993, Source: gatewaycontract.StopSourceAuto})
	if err != nil || !result.Accepted || result.Stopped || result.CommandID != "stop-command" || result.Status != "pending" {
		t.Fatalf("acceptance=%+v err=%v", result, err)
	}
}

// TestClientRejectsInvalidResponses 验证错误状态、缺字段、编号不匹配、矛盾的成功标志和无效正文均被拒绝。
func TestClientRejectsInvalidResponses(t *testing.T) {
	cases := []struct {
		// name 子测试名称。
		name string
		// status 测试服务返回的 HTTP 状态。
		status int
		// body 该场景响应 JSON正文，不修改测试值或业务断言。
		body string
		// stop 为 true 时调用停止接口，否则查询充电记录。
		stop bool
		// want 是预期错误分类，nil 时只检查调用失败。
		want error
	}{
		{"unauthorized", 401, `{}`, false, serviceclient.ErrUnauthorized},
		{"forbidden", 403, `{}`, false, serviceclient.ErrUnauthorized},
		{"wrong HTTP status", 202, `{"code":0,"data":{"evidence":[]}}`, false, ErrInvalidResponse},
		{"missing code", 200, `{"data":{"evidence":[]}}`, false, ErrInvalidResponse},
		{"empty body", 200, ``, false, ErrInvalidResponse},
		{"missing data", 200, `{"code":0}`, false, ErrInvalidResponse},
		{"null data", 200, `{"code":0,"data":null}`, false, ErrInvalidResponse},
		{"wrong count", 200, `{"code":0,"data":{"evidence":[]}}`, false, ErrInvalidResponse},
		{"wrong identity", 200, `{"code":0,"data":{"evidence":[{"device_id":"other","port_no":1,"complete":true}]}}`, false, ErrInvalidResponse},
		{"stop 200 is not acceptance", 200, `{"code":0,"data":{"accepted":true,"stopped":false,"command_id":"cmd","status":"pending"}}`, true, ErrInvalidResponse},
		{"stop not accepted", 202, `{"code":0,"data":{"accepted":false,"command_id":"cmd","status":"pending"}}`, true, ErrInvalidResponse},
		{"stop lacks command", 202, `{"code":0,"data":{"accepted":true,"status":"pending"}}`, true, ErrInvalidResponse},
		{"stop claims completion", 202, `{"code":0,"data":{"accepted":true,"stopped":true,"command_id":"cmd","status":"pending"}}`, true, ErrInvalidResponse},
		{"malformed JSON", 200, `{`, false, nil},
		{"service unavailable", 503, `{"code":5003}`, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			var err error
			if tc.stop {
				_, err = client.RequestStop(context.Background(), gatewaycontract.StopRequest{})
			} else {
				_, err = client.ChargingEvidence(context.Background(), []gatewaycontract.EvidenceQueryRequest{{DeviceID: "D-1", PortNo: 1}})
			}
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
}

// TestClientPreservesBusinessErrorAndCancellation 验证客户端保留业务错误和 context 取消错误。
func TestClientPreservesBusinessErrorAndCancellation(t *testing.T) {
	client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"code":2000,"data":null}`)) })
	_, err := client.ChargingEvidence(context.Background(), []gatewaycontract.EvidenceQueryRequest{{DeviceID: "D-1", PortNo: 1}})
	var rejection BusinessError
	if !errors.As(err, &rejection) || rejection.Code != 2000 {
		t.Fatalf("lost business rejection: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.RequestStop(ctx, gatewaycontract.StopRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

// TestClientRejectsInvalidConfigurationAndBatchWithoutCallingServer 验证无效地址、令牌和查询批次在发送请求前被拒绝。
func TestClientRejectsInvalidConfigurationAndBatchWithoutCallingServer(t *testing.T) {
	for _, tc := range []struct {
		// base,token 是待校验的服务地址和令牌。
		base, token string
	}{
		{"", "token"}, {"ftp://gateway", "token"}, {"http://", "token"},
		{"http://gateway?x=1", "token"}, {"http://gateway#fragment", "token"},
		{"http://user:password@gateway", "token"}, {"http://gateway", ""},
	} {
		if _, err := NewClient(serviceclient.Client{}, tc.base, tc.token); err == nil {
			t.Errorf("accepted invalid client config")
		}
	}
	client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid batch reached server") })
	for _, queries := range [][]gatewaycontract.EvidenceQueryRequest{nil, make([]gatewaycontract.EvidenceQueryRequest, 21)} {
		if _, err := client.ChargingEvidence(context.Background(), queries); err == nil {
			t.Fatal("invalid batch accepted")
		}
	}
}

// TestRequestStartChecksOrderAndReceiptFact 验证启动请求只提交订单号，响应中的 acked 与命令状态一致。
// 本地响应不代表实际设备确认。
func TestRequestStartChecksOrderAndReceiptFact(t *testing.T) {
	for _, status := range []string{"pending", "sent", "stopping", "acked"} {
		t.Run(status, func(t *testing.T) {
			client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/internal/charge-orders/start" || r.Header.Get("X-Service-Token") != "test-service-token" || json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || string(body["order_no"]) != `"C-1"` {
					t.Error("start request differs from contract")
				}
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"order_no": "C-1", "command_id": "cmd-1", "status": status, "device_acknowledged": status == "acked"}})
			})
			data, err := client.RequestStart(context.Background(), gatewaycontract.StartRequest{OrderNo: "C-1"})
			if err != nil || data.OrderNo != "C-1" || data.CommandID != "cmd-1" || data.Status != status || data.DeviceAcknowledged != (status == "acked") {
				t.Fatalf("acceptance=%+v err=%v", data, err)
			}
		})
	}
}

// TestRequestStartRejectsIncompleteAcceptance 验证 HTTP 200、缺字段、编号错误以及无效命令、状态或确认标志均被拒绝。
func TestRequestStartRejectsIncompleteAcceptance(t *testing.T) {
	for _, tc := range []struct {
		// name 子测试名称。
		name string
		// status 测试服务返回的 HTTP 状态。
		status int
		// body 该场景响应 JSON正文，不修改测试值或业务断言。
		body string
	}{
		{"200 is not acceptance", 200, `{"code":0,"data":{"order_no":"C-1","command_id":"cmd","status":"pending"}}`},
		{"missing code", 202, `{"data":{"order_no":"C-1","command_id":"cmd","status":"pending"}}`},
		{"wrong order", 202, `{"code":0,"data":{"order_no":"C-2","command_id":"cmd","status":"pending"}}`},
		{"missing command", 202, `{"code":0,"data":{"order_no":"C-1","status":"pending"}}`},
		{"missing status", 202, `{"code":0,"data":{"order_no":"C-1","command_id":"cmd"}}`},
		{"false device success", 202, `{"code":0,"data":{"order_no":"C-1","command_id":"cmd","status":"pending","device_acknowledged":true}}`},
		{"missing acknowledgment", 202, `{"code":0,"data":{"order_no":"C-1","command_id":"cmd","status":"acked"}}`},
		{"missing data", 202, `{"code":0}`},
		{"malformed JSON", 202, `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := client.RequestStart(context.Background(), gatewaycontract.StartRequest{OrderNo: "C-1"})
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("invalid acceptance passed: %v", err)
			}
		})
	}
}
