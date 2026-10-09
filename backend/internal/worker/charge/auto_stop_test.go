package charge

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

// TestAutoStopConsumerPreservesValidatedProgress 和真实专用客户端核对成功及未完成计数、 wire 和一次调用。
// 回环服务只提供回执，不执行停止命令或资金操作，合法部分计数不能被调用方吞掉。
func TestAutoStopConsumerPreservesValidatedProgress(t *testing.T) {
	for _, tc := range []struct {
		name   string // 成功或未完成回执场景。
		status int    // 回环服务的 HTTP 状态。
		body   string // 端点回执，包含明确处理数量。
		count  int    // 专用客户端已验证、调用方应原样返回的数量。
	}{
		{"completed", 200, `{"code":0,"data":{"stopped":3}}`, 3},
		{"incomplete", 503, `{"code":5003,"data":{"stopped":2}}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != "null" || r.Method != "POST" || r.URL.Path != centralcontract.AutoStopPath ||
					r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Service-Token") != "auto-stop-token" {
					t.Error("original auto-stop wire changed")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client, err := centralclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "auto-stop-token")
			if err != nil {
				t.Fatal(err)
			}
			count, err := (AutoStopper{Central: client}).Run(context.Background())
			if count != tc.count || calls.Load() != 1 || (tc.status == 200) != (err == nil) {
				t.Fatalf("count=%d calls=%d err=%v", count, calls.Load(), err)
			}
			if tc.status == 503 {
				var status *serviceclient.HTTPError
				var business centralcontract.BusinessError
				if !errors.As(err, &status) || status.StatusCode != 503 || !errors.As(err, &business) || business.Code != 5003 {
					t.Fatalf("incomplete error lost: %v", err)
				}
			}
		})
	}
}

// autoStopFixture 仅观测停机调用方的输入并返回指定回复，不执行 HTTP、数据库或设备操作。
type autoStopFixture func(context.Context, centralcontract.AutoStopRequest) (centralcontract.AutoStopReply, error)

// AutoStop 将 ctx 和 request 传给 fixture，原样返回其回复和错误，副作用由 fixture 定义。
func (f autoStopFixture) AutoStop(ctx context.Context, request centralcontract.AutoStopRequest) (centralcontract.AutoStopReply, error) {
	return f(ctx, request)
}

// TestAutoStopConsumerKeepsContextAndDependencyErrors 核对 context、部分计数及错误原样传播，未配置明确失败。
// 返回数量由测试替身提供，未发送设备停止命令。
func TestAutoStopConsumerKeepsContextAndDependencyErrors(t *testing.T) {
	if count, err := (AutoStopper{}).Run(context.Background()); count != 0 || err == nil || err.Error() != "auto stop is not configured" {
		t.Fatalf("unconfigured count=%d err=%v", count, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, want := range []error{context.Canceled, errors.New("original auto-stop failure")} {
		calls := 0
		client := autoStopFixture(func(got context.Context, _ centralcontract.AutoStopRequest) (centralcontract.AutoStopReply, error) {
			calls++
			if got != ctx || got.Err() != context.Canceled {
				t.Error("consumer changed request context")
			}
			return centralcontract.AutoStopReply{Stopped: 2}, want
		})
		if count, err := (AutoStopper{Central: client}).Run(ctx); count != 2 || err != want || calls != 1 {
			t.Fatalf("count=%d calls=%d err=%v want=%v", count, calls, err, want)
		}
	}
}
