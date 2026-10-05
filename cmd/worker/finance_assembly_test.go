package main

import (
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
)

// workerJobTransport 只记录实际cmd装配后的资金请求，不执行外部网络或业务写入。
type workerJobTransport struct {
	paths []string // 同一测试goroutine里的原请求顺序。
	token string   // 预期由具名客户端注入的服务身份。
}

// RoundTrip 核对中央地址与身份并返回端点真实成功形状，未知路径失败；不重试或创建业务任务。
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
	calls int // 同一调用goroutine里的意外请求数量。
}

// RoundTrip 记录调用后拒绝，不访问外部网络。
func (s *workerUnexpectedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	s.calls++
	return nil, errors.New("finance task used internal query transport")
}

// TestAssembledFinanceConsumersUseManagedJobTransport 核对实际assemble向三消费者注入jobHTTP连接池和中央服务身份。
// 不启动后台循环，不调用DB/Redis；原查询连接池应没有资金请求，两个受管理Client自身配置不改变。
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
