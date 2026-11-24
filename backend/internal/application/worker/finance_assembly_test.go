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
	if r.URL.Host != config.CentralServiceHost || r.Header.Get("X-Service-Token") != s.token {
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

// TestAssembledFinanceConsumersUseLocalTransport 验证资金任务使用进程内客户端和中央服务令牌。
func TestAssembledFinanceConsumersUseLocalTransport(t *testing.T) {
	jobs := &workerJobTransport{token: "assembled-job-token"}
	r := &resources{InternalHTTP: &http.Client{Transport: jobs, Timeout: 30 * time.Second}}
	a, err := assemble(config.Worker{ServiceToken: jobs.token}, r)
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
	if !reflect.DeepEqual(jobs.paths, []string{contract.AutoStopPath, contract.BillingDispatchPath, contract.RefundDispatchPath}) {
		t.Fatalf("assembled job paths=%v", jobs.paths)
	}
}
