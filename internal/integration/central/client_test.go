package central

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

// centralForServer 接收 t与 handler，创建本地 HTTP夹具和真实中央客户端并注册服务关闭；返回客户端，配置失败终止测试，不访问数据库。
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

// TestPaidStartsPreservesCursorIdentityAndEmptyScan 使用 t与本地 HTTP夹具验证原服务令牌、超过2^53游标身份及非 nil空页；只验证 wire，不证明真实订单查询。
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

// TestPaidStartsRejectsIncompleteAndInconsistentResponse 使用 t验证本地夹具的状态、缺字段、游标／订单失配及非法JSON均拒绝暴露部分页；不访问真实服务或数据库。
func TestPaidStartsRejectsIncompleteAndInconsistentResponse(t *testing.T) {
	for _, tc := range []struct {
		// name 原子测试场景名，用于定位对应输入及错误断言。
		name string
		// status 该场景模拟服务实际返回的 HTTP 状态，用于原成功／失败契约核对。
		status int
		// body 该场景原响应 JSON正文，不修改测试值或业务断言。
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

// TestPaidStartsPreservesHTTPBusinessAndCancellationErrors 使用 t验证本地客户端保留HTTP、业务码及已取消 context错误，夹具回复不表示真实业务状态。
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

// TestCentralClientRejectsInvalidConfiguration 使用 t验证原地址与令牌构造边界，失败不发网络请求，不配置进程资源。
func TestCentralClientRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		// base,token 分别为待校验的原服务地址和服务令牌；组合用于构造拒绝边界，不向真实服务发请求。
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
