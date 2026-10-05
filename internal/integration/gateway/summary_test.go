package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

// TestSummaryClientRequiresSuccessfulEnvelope 核对 loopback 传输的身份、成功状态和完整 envelope；不代表真实网关数据。
func TestSummaryClientRequiresSuccessfulEnvelope(t *testing.T) {
	for _, fixture := range []struct {
		name   string /* 错误场景 */
		status int    /* 原 HTTP 状态 */
		body   string /* 原响应 JSON */
	}{
		{"wrong-success-status", 201, `{"code":0,"data":{"items":[]}}`},
		{"missing-code", 200, `{"data":{"items":[]}}`},
		{"missing-data", 200, `{"code":0}`},
		{"null-data", 200, `{"code":0,"data":null}`},
		{"business-failure", 200, `{"code":1009,"data":{"items":[]}}`},
		{"malformed", 200, `{"code":0`},
		{"auth-rejected", 401, `{"code":1003}`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != contract.DeviceSummariesPath || r.URL.Query().Get("device_id") != "board&escape" || r.Header.Get("X-Service-Token") != "summary-secret" {
					t.Errorf("wrong identity or query: %s", r.URL)
				}
				w.WriteHeader(fixture.status)
				_, _ = w.Write([]byte(fixture.body))
			}))
			defer server.Close()
			client, err := NewClient(serviceclient.Client{}, server.URL, "summary-secret")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.DeviceSummaries(context.Background(), []string{"board&escape"}); err == nil {
				t.Fatal("invalid envelope accepted")
			}
		})
	}
}

// TestSummaryClientCancelsInFlightRead 验证真实 loopback 请求已进入处理器后取消父 ctx 能结束调用，不使用固定延时。
func TestSummaryClientCancelsInFlightRead(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer server.Close()
	client, err := NewClient(serviceclient.Client{}, server.URL, "summary-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := client.DeviceSummaries(ctx, []string{"board"}); result <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("summary request not entered")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("summary cancellation did not terminate")
	}
}
