package billing

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

// TestBillingDispatchCallsProtectedRoute 用 t 和具名客户端核对原计费 POST、路径和令牌，回环服务不执行资金。
func TestBillingDispatchCallsProtectedRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != 0 || r.Header.Get("Content-Type") != "" || r.Method != "POST" || r.URL.Path != centralcontract.BillingDispatchPath || r.Header.Get("X-Service-Token") != "local-billing-test" {
			t.Error("wrong internal billing request")
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"completed":1}}`))
	}))
	defer server.Close()
	client, err := centralclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "local-billing-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := (Dispatcher{Central: client}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestBillingDispatchPreservesIncompleteError 用 t 核对合法部分计费回执仍作为失败传播且只请求一次；不执行真实计费。
func TestBillingDispatchPreservesIncompleteError(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"code":5003,"data":{"completed":1}}`)
	}))
	defer server.Close()
	client, err := centralclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "local-billing-test")
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

// billingDispatchFixture 仅观测消费方传入的 context 和请求并返回指定回执，不访问网络或资金库。
type billingDispatchFixture func(context.Context, centralcontract.BillingDispatchRequest) (centralcontract.BillingDispatchReply, error)

// DispatchBilling 将 ctx 和 request 交给 fixture 函数，原样返回回复和错误，副作用由该函数定义。
func (f billingDispatchFixture) DispatchBilling(ctx context.Context, request centralcontract.BillingDispatchRequest) (centralcontract.BillingDispatchReply, error) {
	return f(ctx, request)
}

// TestBillingDispatchKeepsContextAndDependencyErrors 用 t 核对取消链、原错误及未配置失败；窄 fixture 不代表客户端或资金执行。
func TestBillingDispatchKeepsContextAndDependencyErrors(t *testing.T) {
	if err := (Dispatcher{}).Run(context.Background()); err == nil || err.Error() != "billing dispatcher is not configured" {
		t.Fatalf("unconfigured dispatcher=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, want := range []error{context.Canceled, errors.New("original billing failure")} {
		calls := 0
		client := billingDispatchFixture(func(got context.Context, _ centralcontract.BillingDispatchRequest) (centralcontract.BillingDispatchReply, error) {
			calls++
			if got != ctx || got.Err() != context.Canceled {
				t.Error("consumer changed request context")
			}
			return centralcontract.BillingDispatchReply{Completed: 1}, want
		})
		if err := (Dispatcher{Central: client}).Run(ctx); err != want || calls != 1 {
			t.Fatalf("calls=%d err=%v want=%v", calls, err, want)
		}
	}
}
