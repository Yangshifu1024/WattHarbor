package worker

import (
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	contract "wattharbor/internal/contracts/central"
	"wattharbor/internal/platform/config"
)

// workerJobTransport 检查启动代码创建的客户端请求，并返回固定响应。
type workerJobTransport struct {
	paths []string
	token string
}

// RoundTrip 核对服务地址、令牌和路径，返回对应任务的成功 JSON；不发送网络请求。
func (s *workerJobTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "central.invalid" || r.Header.Get("X-Service-Token") != s.token {
		return nil, errors.New("assembled finance client used wrong address or identity")
	}
	s.paths = append(s.paths, r.URL.Path)
	var payload string
	switch r.URL.Path {
	case contract.AutoStopPath:
		payload = `{"code":0,"data":{"stopped":0}}`
	case contract.BillingDispatchPath:
		payload = `{"code":0,"data":{"completed":0}}`
	case contract.RefundDispatchPath:
		payload = `{"code":0,"data":{"enabled":false,"processed":0}}`
	default:
		return nil, errors.New("unexpected assembled finance path")
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
}

// workerUnexpectedTransport 记录错误连接池使用，任何资金请求进入查询连接池即失败。
type workerUnexpectedTransport struct {
	calls int
}

// RoundTrip 记录调用后拒绝，不访问外部网络。

func (s *workerUnexpectedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	s.calls++
	return nil, errors.New("finance task used internal query transport")
}

// TestAssembledFinanceConsumersUseManagedJobTransport 验证三个资金任务使用 JobHTTP 和 central 服务令牌，查询连接池不会收到资金请求。
func TestAssembledFinanceConsumersUseManagedJobTransport(t *testing.T) {
	jobs := &workerJobTransport{token: "assembled-job-token"}
	queries := &workerUnexpectedTransport{}
	r := &resources{JobHTTP: &http.Client{Transport: jobs, Timeout: 30 * time.Second}, InternalHTTP: &http.Client{Transport: queries, Timeout: 5 * time.Second}}
	a, err := assemble(config.Worker{CentralInternalURL: "http://central.invalid", GatewayInternalURL: "http://gateway.invalid", ServiceToken: jobs.token}, r)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := a.Jobs.AutoStops.Run(t.Context()); err != nil || count != 0 {
		t.Fatalf("assembled stop=%d: %v", count, err)
	}
	if err := a.Jobs.Billing.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.Jobs.Refunds.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(jobs.paths, []string{contract.AutoStopPath, contract.BillingDispatchPath, contract.RefundDispatchPath}) || queries.calls != 0 ||
		r.JobHTTP.Transport != jobs || r.InternalHTTP.Transport != queries || r.JobHTTP.Timeout != 30*time.Second || r.InternalHTTP.Timeout != 5*time.Second {
		t.Fatalf("assembled job paths=%v query calls=%d, managed client configuration changed", jobs.paths, queries.calls)
	}
}
