package central

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"chargepilot/internal/platform/serviceclient"
)

// centralForServer 为测试创建本地 HTTP 服务和 central 客户端，并注册服务清理。
func centralForServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL+"/", "secret")
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestPaidStartsPreservesCursorIdentityAndEmptyScan 验证请求携带服务令牌，超过 2^53 的游标不丢精度，无订单时返回非 nil 空列表。
// 使用本地 HTTP 服务，不验证真实订单查询。
func TestPaidStartsPreservesCursorIdentityAndEmptyScan(t *testing.T) {
	client := centralForServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/internal/charge-orders/paid-starts" || r.Header.Get("X-Service-Token") != "secret" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		switch r.URL.Query().Get("after_id") {
		case "9007199254740993":
			fmt.Fprint(w, `{"code":0,"data":{"items":[{"id":9007199254740994,"order_no":"C-1"}]}}`)
		case "9007199254740994":
			fmt.Fprint(w, `{"code":0,"data":{"items":[]}}`)
		default:
			t.Error("lost uint64 cursor")
		}
	})
	rows, err := client.PaidStarts(context.Background(), 9007199254740993)
	if err != nil || len(rows) != 1 || rows[0].ID != 9007199254740994 || rows[0].OrderNo != "C-1" {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	rows, err = client.PaidStarts(context.Background(), rows[0].ID)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty scan: rows=%v err=%v", rows, err)
	}
}

// TestPaidStartsRejectsIncompleteAndInconsistentResponse 验证错误状态、缺字段、游标或订单不匹配、无效 JSON 均被拒绝，不返回部分页面。
func TestPaidStartsRejectsIncompleteAndInconsistentResponse(t *testing.T) {
	for _, tc := range []struct {
		// name 子测试名称。
		name string
		// status 测试服务返回的 HTTP 状态。
		status int
		// body 该场景响应 JSON正文，不修改测试值或业务断言。
		body string
	}{
		{"wrong HTTP status", 202, `{"code":0,"data":{"items":[]}}`},
		{"missing code", 200, `{"data":{"items":[]}}`},
		{"missing data", 200, `{"code":0}`},
		{"null items", 200, `{"code":0,"data":{"items":null}}`},
		{"missing items", 200, `{"code":0,"data":{}}`},
		{"same cursor", 200, `{"code":0,"data":{"items":[{"id":5,"order_no":"C-5"}]}}`},
		{"unordered cursor", 200, `{"code":0,"data":{"items":[{"id":8,"order_no":"C-8"},{"id":7,"order_no":"C-7"}]}}`},
		{"missing order", 200, `{"code":0,"data":{"items":[{"id":8}]}}`},
		{"malformed JSON", 200, `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := centralForServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) })
			rows, err := client.PaidStarts(context.Background(), 5)
			if !errors.Is(err, ErrInvalidResponse) || rows != nil {
				t.Fatalf("partial data exposed: rows=%v err=%v", rows, err)
			}
		})
	}
}

// TestPaidStartsPreservesHTTPBusinessAndCancellationErrors 验证客户端保留 HTTP、业务码和 context 取消错误。
func TestPaidStartsPreservesHTTPBusinessAndCancellationErrors(t *testing.T) {
	client := centralForServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after_id") == "1" {
			w.WriteHeader(409)
			return
		}
		fmt.Fprint(w, `{"code":5001,"data":null}`)
	})
	_, err := client.PaidStarts(context.Background(), 0)
	var business BusinessError
	if !errors.As(err, &business) || business.Code != 5001 {
		t.Fatalf("business error=%v", err)
	}
	_, err = client.PaidStarts(context.Background(), 1)
	var rejection *serviceclient.HTTPError
	if !errors.As(err, &rejection) || rejection.StatusCode != 409 {
		t.Fatalf("HTTP error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.PaidStarts(ctx, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

// TestCentralClientRejectsInvalidConfiguration 验证无效服务地址或令牌在发送请求前被拒绝。
func TestCentralClientRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		// base,token 是待校验的服务地址和令牌。
		base, token string
	}{
		{"", "token"}, {"ftp://central", "token"}, {"http://", "token"},
		{"http://central?x=1", "token"}, {"http://central#fragment", "token"},
		{"http://user:password@central", "token"}, {"http://central", ""},
	} {
		if _, err := NewClient(serviceclient.Client{}, tc.base, tc.token); err == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
}
