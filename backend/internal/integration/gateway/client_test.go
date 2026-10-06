package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

// clientForServer 接收 t与 handler，创建本地 HTTP服务及真实网关客户端并登记服务关闭，返回客户端；失败终止测试，不访问数据库或设备。
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

// TestChargingEvidencePreservesWireFieldsAndCursor 使用 t核对实际本地HTTP请求的设备／端口／启动时刻／游标及回复采样原单位、完整标志和下一游标；不证明真库过滤。
func TestChargingEvidencePreservesWireFieldsAndCursor(t *testing.T) {
	started := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	client := clientForServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/internal/charging-evidence" || r.Header.Get("X-Service-Token") != "test-service-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: method=%s path=%s", r.Method, r.URL.Path)
		}
		var body struct {
			// Requests 原批量证据查询字段集合，供JSON夹具核对，不创建业务请求之外的状态。
			Requests []struct {
				// Device 原设备业务编号字符串，必须保持传输身份。
				Device string `json:"device_id"`
				// Port 原端口序号，从1开始，不改协议数值。
				Port uint8 `json:"port_no"`
				// Start 原证据查询启动时刻，保留 time.Time值用于比较。
				Start time.Time `json:"started_at"`
				// After 原uint64分页水印，不经float64。
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

// TestRequestStopRequiresAcceptedResponse 使用 t核对原停止请求大整数身份／来源与202受理，回复 stopped=false；本地夹具不证明设备停机或资金变化。
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

// TestClientRejectsInvalidResponses 使用 t验证原证据及停机客户端对状态、缺字段、失配、错误成功声明和非法正文的拒绝；不访问真实业务服务。
func TestClientRejectsInvalidResponses(t *testing.T) {
	cases := []struct {
		// name 原子测试场景名，用于定位对应输入及错误断言。
		name string
		// status 该场景模拟服务实际返回的 HTTP 状态，用于原成功／失败契约核对。
		status int
		// body 该场景原响应 JSON正文，不修改测试值或业务断言。
		body string
		// stop 为true时走停止受理客户端，否则走证据查询，保持原测试分支。
		stop bool
		// want 该场景预期错误分类，nil时仅要求原测试指定的失败条件。
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

// TestClientPreservesBusinessErrorAndCancellation 使用 t验证本地HTTP业务码和已取消context的错误保留，不把夹具回复视为持久完成。
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

// TestClientRejectsInvalidConfigurationAndBatchWithoutCallingServer 使用 t验证原地址／令牌和证据批次数边界；非法批次不得到达本地服务，无数据库或设备 IO。
func TestClientRejectsInvalidConfigurationAndBatchWithoutCallingServer(t *testing.T) {
	for _, tc := range []struct {
		// base,token 分别为待校验的原服务地址和服务令牌；组合用于构造拒绝边界，不向真实服务发请求。
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

// TestRequestStartChecksOrderAndReceiptFact 使用 t核对原仅订单号START请求及各受理状态的 acked一致性，夹具只提供wire事实，不代表真实设备确认。
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

// TestRequestStartRejectsIncompleteAcceptance 使用 t验证原START客户端拒绝HTTP200、缺失或错误身份、命令、状态及确认标志；不访问数据库或实机。
func TestRequestStartRejectsIncompleteAcceptance(t *testing.T) {
	for _, tc := range []struct {
		// name 原子测试场景名，用于定位对应输入及错误断言。
		name string
		// status 该场景模拟服务实际返回的 HTTP 状态，用于原成功／失败契约核对。
		status int
		// body 该场景原响应 JSON正文，不修改测试值或业务断言。
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
