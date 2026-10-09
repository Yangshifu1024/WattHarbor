package refund

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	centralcontract "chargepilot/internal/contracts/central"
	centralclient "chargepilot/internal/integration/central"
	"chargepilot/internal/platform/serviceclient"
)

// TestDispatcherRequiresSuccessfulEnvelope 使用专用客户端核对退款 POST 及有效回执；回环响应不执行退款。
// code=0 却无 data 的报文必须失败，成功需明确 enabled 与非负 processed。
func TestDispatcherRequiresSuccessfulEnvelope(t *testing.T) {
	valid := `{"code":0,"data":{"enabled":false,"processed":0}}`
	for _, body := range []string{valid, `{"code":0}`, `{"code":5001}`, `{}`, `not json`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				payload, err := io.ReadAll(r.Body)
				if err != nil || len(payload) != 0 || r.Header.Get("Content-Type") != "" || r.Method != "POST" || r.URL.Path != centralcontract.RefundDispatchPath || r.Header.Get("X-Service-Token") != "test-token" {
					t.Error("request mismatch")
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client, err := centralclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "test-token")
			if err != nil {
				t.Fatal(err)
			}
			err = (Dispatcher{Central: client}).Run(context.Background())
			if (err == nil) != (body == valid) {
				t.Fatal(err)
			}
		})
	}
}

// TestRefundDispatchPreservesIncompleteError 核对合法部分退款回执仍传播错误且只请求一次；不调用真实退款渠道。
func TestRefundDispatchPreservesIncompleteError(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"code":5003,"data":{"processed":1}}`)
	}))
	defer server.Close()
	client, err := centralclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	err = (Dispatcher{Central: client}).Run(context.Background())
	var status *serviceclient.HTTPError
	var business centralcontract.BusinessError
	if !errors.As(err, &status) || status.StatusCode != 503 || !errors.As(err, &business) || business.Code != 5003 || calls.Load() != 1 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

// refundDispatchFixture 仅观测调用方传入的 context 和请求并返回指定回执，不访问网络或资金库。
type refundDispatchFixture func(context.Context, centralcontract.RefundDispatchRequest) (centralcontract.RefundDispatchReply, error)

// DispatchRefunds 将 ctx 和 request 交给 fixture 函数，原样返回回复和错误，副作用由该函数定义。
func (f refundDispatchFixture) DispatchRefunds(ctx context.Context, request centralcontract.RefundDispatchRequest) (centralcontract.RefundDispatchReply, error) {
	return f(ctx, request)
}

// TestRefundDispatchKeepsContextAndDependencyErrors 核对取消链、错误及未配置失败；测试替身 不代表渠道执行。
func TestRefundDispatchKeepsContextAndDependencyErrors(t *testing.T) {
	if err := (Dispatcher{}).Run(context.Background()); err == nil || err.Error() != "refund dispatcher is not configured" {
		t.Fatalf("unconfigured dispatcher=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, want := range []error{context.Canceled, errors.New("original refund failure")} {
		calls := 0
		client := refundDispatchFixture(func(got context.Context, _ centralcontract.RefundDispatchRequest) (centralcontract.RefundDispatchReply, error) {
			calls++
			if got != ctx || got.Err() != context.Canceled {
				t.Error("consumer changed request context")
			}
			return centralcontract.RefundDispatchReply{Processed: 1}, want
		})
		if err := (Dispatcher{Central: client}).Run(ctx); err != want || calls != 1 {
			t.Fatalf("calls=%d err=%v want=%v", calls, err, want)
		}
	}
}
