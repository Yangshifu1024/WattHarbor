package central_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	centralcontract "chargepilot/internal/contracts/central"
	centralhttp "chargepilot/internal/http/central"
	centralclient "chargepilot/internal/integration/central"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
)

// registeredJobStopper 仅提供停机数量与错误的窄能力fixture，不访问订单、计费库或真实设备。
type registeredJobStopper struct {
	progress  int                // Run 返回的原扫描处理数量，测试期间不变。
	failure   error              // Run 返回的原未完成错误，测试期间不变。
	calls     atomic.Int64       // 已执行业务扫描的次数，用于证明服务令牌先行拒绝。
	remaining chan time.Duration // 捕获真实注册处理器派生的context期限；只传递时长，不持有请求context。
}

// Run 接收真实HTTP处理器派生的ctx，记录调用及期限，返回fixture的progress和failure。
// 不执行资金、停止命令或数据库IO；只能证明装配、令牌顺序、期限与公开回执契约。
func (f *registeredJobStopper) Run(ctx context.Context) (int, error) {
	f.calls.Add(1)
	deadline, ok := ctx.Deadline()
	if ok {
		f.remaining <- time.Until(deadline)
	} else {
		f.remaining <- 0
	}
	return f.progress, f.failure
}

// TestRegisteredJobContracts 用t和真实Register路由核对具名客户端的停机回执与退款禁用回执。
// 三条资金任务路由均须先拒绝坏令牌；计费依赖刻意未装配，仅核对其鉴权，完整计费由真库测试验收。
func TestRegisteredJobContracts(t *testing.T) {
	for _, tc := range []struct {
		name     string // 停机扫描成功或未完成场景。
		progress int    // fixture声明的合法处理数量。
		failure  error  // fixture声明的未完成错误，不包含真实设备状态。
	}{
		{"completed", 3, nil},
		{"incomplete", 2, errors.New("stop fixture incomplete")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stopper := &registeredJobStopper{progress: tc.progress, failure: tc.failure, remaining: make(chan time.Duration, 1)}
			router := httpapi.NewRouter()
			centralhttp.BillingAPI{ServiceToken: "jobs-token"}.Register(router)
			centralhttp.RefundAPI{ServiceToken: "jobs-token"}.Register(router)
			centralhttp.AutoStopAPI{Stopper: stopper, ServiceToken: "jobs-token"}.Register(router)
			server := httptest.NewServer(router)
			defer server.Close()
			bad, err := centralclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "wrong-token")
			if err != nil {
				t.Fatal(err)
			}
			billing, err := bad.DispatchBilling(context.Background(), centralcontract.BillingDispatchRequest{})
			if !errors.Is(err, serviceclient.ErrUnauthorized) || billing.Completed != 0 {
				t.Fatalf("billing rejection=%+v %v", billing, err)
			}
			refund, err := bad.DispatchRefunds(context.Background(), centralcontract.RefundDispatchRequest{})
			if !errors.Is(err, serviceclient.ErrUnauthorized) || refund.Processed != 0 || refund.Enabled != nil {
				t.Fatalf("refund rejection=%+v %v", refund, err)
			}
			stop, err := bad.AutoStop(context.Background(), centralcontract.AutoStopRequest{})
			if !errors.Is(err, serviceclient.ErrUnauthorized) || stop.Stopped != 0 || stopper.calls.Load() != 0 {
				t.Fatalf("stop rejection=%+v calls=%d err=%v", stop, stopper.calls.Load(), err)
			}
			client, err := centralclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "jobs-token")
			if err != nil {
				t.Fatal(err)
			}
			refund, err = client.DispatchRefunds(context.Background(), centralcontract.RefundDispatchRequest{})
			if err != nil || refund.Processed != 0 || refund.Enabled == nil || *refund.Enabled {
				t.Fatalf("disabled refund=%+v err=%v", refund, err)
			}
			stop, err = client.AutoStop(context.Background(), centralcontract.AutoStopRequest{})
			if stop.Stopped != tc.progress || stopper.calls.Load() != 1 || (tc.failure == nil) != (err == nil) {
				t.Fatalf("stop=%+v calls=%d err=%v", stop, stopper.calls.Load(), err)
			}
			if tc.failure != nil {
				var statusErr *serviceclient.HTTPError
				var business centralcontract.BusinessError
				if !errors.As(err, &statusErr) || statusErr.StatusCode != 503 || !errors.As(err, &business) || business.Code != 5003 {
					t.Fatalf("incomplete error contract=%v", err)
				}
			}
			if remaining := <-stopper.remaining; remaining < 24*time.Second || remaining > 25*time.Second {
				t.Fatalf("original HTTP scan deadline=%s", remaining)
			}
		})
	}
}
