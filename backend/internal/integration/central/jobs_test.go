package central_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	centralcontract "chargepilot/internal/contracts/central"
	centralclient "chargepilot/internal/integration/central"
	"chargepilot/internal/platform/serviceclient"
)

// jobKind 是测试中的端点选择，不参与生产客户端或资金算法。
type jobKind struct {
	name  string
	path  string
	field string
}

// jobKinds 返回三种端点的静态测试清单，无网络或资金副作用。
func jobKinds() []jobKind {
	return []jobKind{
		{"billing", centralcontract.BillingDispatchPath, "completed"},
		{"refund", centralcontract.RefundDispatchPath, "processed"},
		{"auto-stop", centralcontract.AutoStopPath, "stopped"},
	}
}

// callJob 按测试类型调用计费、退款或停机客户端，统一返回进度、启用标志和错误。
func callJob(ctx context.Context, client *centralclient.Client, kind jobKind) (int, *bool, error) {
	switch kind.name {
	case "billing":
		reply, err := client.DispatchBilling(ctx, centralcontract.BillingDispatchRequest{})
		return reply.Completed, nil, err
	case "refund":
		reply, err := client.DispatchRefunds(ctx, centralcontract.RefundDispatchRequest{})
		return reply.Processed, reply.Enabled, err
	default:
		reply, err := client.AutoStop(ctx, centralcontract.AutoStopRequest{})
		return reply.Stopped, nil, err
	}
}

// newJobClient 用 server 的测试连接池和服务令牌装配真实客户端，t 报告配置错误；不连接开发服务。
func newJobClient(t *testing.T, server *httptest.Server) *centralclient.Client {
	t.Helper()
	client, err := centralclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "jobs-token")
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestJobClientsPreserveWireAndPartialProgress 验证任务 POST 的正文、零进度和部分进度。
// 仅采信约定的未完成响应，本地 HTTP 测试不执行真实资金任务。
func TestJobClientsPreserveWireAndPartialProgress(t *testing.T) {
	for _, kind := range jobKinds() {
		t.Run(kind.name, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				status   int
				code     int
				progress int
			}{
				{"success", 200, 0, 3}, {"zero", 200, 0, 0},
				{"incomplete", 503, 5003, 2}, {"business-rejected", 200, 2009, 1},
				{"unauthorized-with-progress", 401, 1001, 1}, {"not-found-with-progress", 404, 1004, 1},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var calls atomic.Int64
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						body, err := io.ReadAll(r.Body)
						wantBody, wantType := "", ""
						if kind.name == "auto-stop" {
							wantBody, wantType = "null", "application/json"
						}
						if err != nil || r.Method != "POST" || r.URL.RequestURI() != kind.path ||
							r.Header.Get("X-Service-Token") != "jobs-token" || string(body) != wantBody ||
							r.Header.Get("Content-Type") != wantType || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Request-ID") != "" {
							t.Errorf("wire method=%s target=%s body=%q type=%q err=%v", r.Method, r.URL.RequestURI(), body, r.Header.Get("Content-Type"), err)
						}
						enabled := ""
						if kind.name == "refund" && tc.status == 200 && tc.code == 0 {
							enabled = `,"enabled":true`
						}
						w.WriteHeader(tc.status)
						_, _ = fmt.Fprintf(w, `{"code":%d,"data":{"%s":%d%s}}`, tc.code, kind.field, tc.progress, enabled)
					}))
					defer server.Close()
					progress, enabled, err := callJob(context.Background(), newJobClient(t, server), kind)
					wantProgress := tc.progress
					if (tc.status != 200 || tc.code != 0) && (tc.status != 503 || tc.code != 5003) {
						wantProgress = 0
					}
					if progress != wantProgress || calls.Load() != 1 {
						t.Fatalf("progress=%d calls=%d err=%v", progress, calls.Load(), err)
					}
					if (tc.status == 200 && tc.code == 0) != (err == nil) {
						t.Fatalf("status=%d code=%d err=%v", tc.status, tc.code, err)
					}
					if kind.name == "refund" && tc.status == 200 && tc.code == 0 && (enabled == nil || !*enabled) {
						t.Fatal("successful refund lacks enabled=true")
					}
					if kind.name == "refund" && (tc.status != 200 || tc.code != 0) && enabled != nil {
						t.Fatal("partial refund invented enabled")
					}
					if tc.status != 200 {
						var statusErr *serviceclient.HTTPError
						if !errors.As(err, &statusErr) || statusErr.StatusCode != tc.status || statusErr.Path != kind.path {
							t.Fatalf("HTTP error lost: %v", err)
						}
					}
					if tc.code != 0 {
						var business centralcontract.BusinessError
						if !errors.As(err, &business) || business.Code != tc.code {
							t.Fatalf("business error lost: %v", err)
						}
					}
					if tc.status == 401 && !errors.Is(err, serviceclient.ErrUnauthorized) || tc.status == 404 && !errors.Is(err, serviceclient.ErrNotFound) {
						t.Fatalf("HTTP sentinel lost: %v", err)
					}
				})
			}
		})
	}
}

// TestJobClientsRejectInvalidReplies 核对缺失、null、负数、整数溢出、非完成状态和正文上限。
// 本地服务返回无效响应，客户端应报错并返回零进度。
func TestJobClientsRejectInvalidReplies(t *testing.T) {
	for _, kind := range jobKinds() {
		t.Run(kind.name, func(t *testing.T) {
			validData := fmt.Sprintf(`{"%s":2}`, kind.field)
			if kind.name == "refund" {
				validData = `{"processed":2,"enabled":true}`
			}
			valid := `{"code":0,"data":` + validData + `}`
			for _, tc := range []struct {
				name   string
				body   string
				status int
			}{
				{"empty", "", 0}, {"null-envelope", "null", 0}, {"array-envelope", "[]", 0},
				{"missing-code", `{"data":` + validData + `}`, 0},
				{"null-code", `{"code":null,"data":` + validData + `}`, 0},
				{"string-code", `{"code":"0","data":` + validData + `}`, 0},
				{"overflow-code", `{"code":999999999999999999999999,"data":` + validData + `}`, 0},
				{"missing-data", `{"code":0}`, 0}, {"null-data", `{"code":0,"data":null}`, 0},
				{"array-data", `{"code":0,"data":[]}`, 0}, {"empty-data", `{"code":0,"data":{}}`, 0},
				{"null-progress", fmt.Sprintf(`{"code":0,"data":{"%s":null}}`, kind.field), 0},
				{"negative-progress", fmt.Sprintf(`{"code":0,"data":{"%s":-1}}`, kind.field), 0},
				{"fractional-progress", fmt.Sprintf(`{"code":0,"data":{"%s":1.2}}`, kind.field), 0},
				{"string-progress", fmt.Sprintf(`{"code":0,"data":{"%s":"2"}}`, kind.field), 0},
				{"overflow-progress", fmt.Sprintf(`{"code":0,"data":{"%s":999999999999999999999999}}`, kind.field), 0},
				{"two-json-values", valid + valid, 0}, {"trailing-junk", valid + " garbage", 0},
				{"body-over-limit", valid + strings.Repeat(" ", 4097-len(valid)), 0},
				{"incomplete-body-over-limit", valid + strings.Repeat(" ", 4097-len(valid)), 503},
				{"accepted-not-completed", valid, 202}, {"no-content", "", 204},
				{"unavailable-with-success-code", valid, 503},
				{"redirect-with-fake-progress", valid, 302},
				{"unauthorized-with-fake-progress", `{"code":1001,"data":` + validData + `}`, 401},
				{"incomplete-with-missing-progress", `{"code":5003,"data":{}}`, 503},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var calls atomic.Int64
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						if tc.status != 0 {
							w.WriteHeader(tc.status)
						}
						_, _ = io.WriteString(w, tc.body)
					}))
					defer server.Close()
					progress, enabled, err := callJob(context.Background(), newJobClient(t, server), kind)
					if !errors.Is(err, centralcontract.ErrInvalidResponse) || progress != 0 || enabled != nil || calls.Load() != 1 {
						t.Fatalf("progress=%d enabled=%v calls=%d err=%v", progress, enabled, calls.Load(), err)
					}
					if tc.status == 401 && !errors.Is(err, serviceclient.ErrUnauthorized) {
						t.Fatalf("unauthorized sentinel lost: %v", err)
					}
				})
			}
			// 上限处的合法单 JSON 必须接受，不能把4096字节自身当成截断。
			t.Run("exact-byte-limit", func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.WriteString(w, valid+strings.Repeat(" ", 4096-len(valid)))
				}))
				defer server.Close()
				progress, _, err := callJob(context.Background(), newJobClient(t, server), kind)
				if err != nil || progress != 2 {
					t.Fatalf("progress=%d err=%v", progress, err)
				}
			})
		})
	}
}

// TestJobClientsRejectTruncatedPartialBodies 模拟收到 HTTP 503 后正文提前结束。
// 即使已收到完整的进度 JSON，也应因正文长度不足而返回错误和零进度。
func TestJobClientsRejectTruncatedPartialBodies(t *testing.T) {
	for _, kind := range jobKinds() {
		t.Run(kind.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Length", "1024")
				w.WriteHeader(503)
				_, _ = fmt.Fprintf(w, `{"code":5003,"data":{"%s":2}}`, kind.field)
			}))
			defer server.Close()
			progress, enabled, err := callJob(context.Background(), newJobClient(t, server), kind)
			var status *serviceclient.HTTPError
			if progress != 0 || enabled != nil || calls.Load() != 1 || !errors.Is(err, io.ErrUnexpectedEOF) ||
				!errors.Is(err, centralcontract.ErrInvalidResponse) || !errors.As(err, &status) || status.StatusCode != 503 {
				t.Fatalf("progress=%d enabled=%v calls=%d err=%v", progress, enabled, calls.Load(), err)
			}
		})
	}
}

// TestRefundClientRequiresExplicitSuccessState 验证退款成功响应必须含布尔 enabled；禁用时进度为零，失败响应可缺少 enabled。
func TestRefundClientRequiresExplicitSuccessState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		data   string
		status int
		valid  bool
	}{
		{"disabled-zero", `{"processed":0,"enabled":false}`, 200, true},
		{"missing-success-state", `{"processed":0}`, 200, false},
		{"null-success-state", `{"processed":0,"enabled":null}`, 200, false},
		{"string-state", `{"processed":0,"enabled":"true"}`, 200, false},
		{"numeric-state", `{"processed":0,"enabled":1}`, 200, false},
		{"disabled-positive-progress", `{"processed":1,"enabled":false}`, 200, false},
		{"missing-failure-state", `{"processed":1}`, 503, true},
		{"null-failure-state", `{"processed":1,"enabled":null}`, 503, false},
		{"disabled-failure-progress", `{"processed":1,"enabled":false}`, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				code := 0
				if tc.status != 200 {
					code = 5003
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, `{"code":%d,"data":%s}`, code, tc.data)
			}))
			defer server.Close()
			reply, err := newJobClient(t, server).DispatchRefunds(context.Background(), centralcontract.RefundDispatchRequest{})
			if errors.Is(err, centralcontract.ErrInvalidResponse) == tc.valid {
				t.Fatalf("valid=%t reply=%+v err=%v", tc.valid, reply, err)
			}
			if tc.name == "disabled-zero" && (err != nil || reply.Enabled == nil || *reply.Enabled || reply.Processed != 0) {
				t.Fatalf("disabled reply=%+v err=%v", reply, err)
			}
			if tc.name == "missing-failure-state" && (err == nil || reply.Enabled != nil || reply.Processed != 1) {
				t.Fatalf("partial reply=%+v err=%v", reply, err)
			}
			if !tc.valid && (reply.Enabled != nil || reply.Processed != 0) {
				t.Fatalf("invented progress: %+v", reply)
			}
		})
	}
}

// jobRoundTripper 将请求交给测试函数，用于检查超时和重定向设置。
type jobRoundTripper func(*http.Request) (*http.Response, error)

func (f jobRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// TestJobClientTimeoutAndRedirectPolicy 验证请求和客户端均使用三十秒超时且共享连接池，重定向不能转发服务令牌。
func TestJobClientTimeoutAndRedirectPolicy(t *testing.T) {
	for _, kind := range jobKinds() {
		t.Run(kind.name, func(t *testing.T) {
			var redirected, originalRedirect atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(http.StatusTemporaryRedirect)
			}))
			defer server.Close()
			remaining := make(chan time.Duration, 1)
			transport := server.Client().Transport
			observed := jobRoundTripper(func(r *http.Request) (*http.Response, error) {
				deadline, ok := r.Context().Deadline()
				if !ok {
					t.Error("request has no managed deadline")
				}
				remaining <- time.Until(deadline)
				return transport.RoundTrip(r)
			})
			checkRedirect := func(*http.Request, []*http.Request) error { originalRedirect.Add(1); return nil }
			shared := &http.Client{Transport: observed, Timeout: time.Millisecond, CheckRedirect: checkRedirect}
			client, err := centralclient.NewClient(serviceclient.Client{HTTP: shared, Timeout: time.Millisecond}, server.URL, "jobs-token")
			if err != nil {
				t.Fatal(err)
			}
			progress, enabled, err := callJob(context.Background(), client, kind)
			var status *serviceclient.HTTPError
			if !errors.As(err, &status) || status.StatusCode != http.StatusTemporaryRedirect || progress != 0 || enabled != nil || redirected.Load() != 0 || originalRedirect.Load() != 0 {
				t.Fatalf("redirected=%d original=%d progress=%d err=%v", redirected.Load(), originalRedirect.Load(), progress, err)
			}
			if duration := <-remaining; duration < 29*time.Second || duration > 30*time.Second {
				t.Fatalf("request deadline=%s", duration)
			}
			if shared.Timeout != time.Millisecond || reflect.ValueOf(shared.Transport).Pointer() != reflect.ValueOf(observed).Pointer() ||
				reflect.ValueOf(shared.CheckRedirect).Pointer() != reflect.ValueOf(checkRedirect).Pointer() {
				t.Fatal("shared HTTP client or transport mutated")
			}
		})
	}
}

// TestJobClientsPropagateCancellation 验证调用方取消和更短超时会结束请求，失败时不凭空生成进度。
func TestJobClientsPropagateCancellation(t *testing.T) {
	for _, kind := range jobKinds() {
		for _, mode := range []string{"cancel", "short-deadline"} {
			t.Run(kind.name+"/"+mode, func(t *testing.T) {
				entered := make(chan struct{}, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					entered <- struct{}{}
					<-r.Context().Done()
				}))
				defer server.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "short-deadline" {
					var deadlineCancel context.CancelFunc
					ctx, deadlineCancel = context.WithTimeout(ctx, 250*time.Millisecond)
					defer deadlineCancel()
				}
				client := newJobClient(t, server)
				result := make(chan error, 1)
				go func() {
					progress, enabled, err := callJob(ctx, client, kind)
					if progress != 0 || enabled != nil {
						t.Errorf("cancelled call invented progress=%d enabled=%v", progress, enabled)
					}
					result <- err
				}()
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("request did not reach loopback gate")
				}
				want := context.DeadlineExceeded
				if mode == "cancel" {
					cancel()
					want = context.Canceled
				}
				select {
				case err := <-result:
					if !errors.Is(err, want) {
						t.Fatalf("err=%v want=%v", err, want)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("client did not honor request cancellation")
				}
			})
		}
	}
}

// TestJobClientsDoNotRetryUnknownResults 让回环服务收到POST后关闭连接，验证未知结果仅返回错误和零进度。
// 每次显式调用只送达一次，无自动补发或新请求身份；此测试不能证明资金是否已经执行。
func TestJobClientsDoNotRetryUnknownResults(t *testing.T) {
	for _, kind := range jobKinds() {
		t.Run(kind.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				if r.Header.Get("X-Request-ID") != "" || r.Header.Get("Idempotency-Key") != "" {
					t.Error("job client invented a request identity")
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			}))
			defer server.Close()
			client := newJobClient(t, server)
			for attempt := 1; attempt <= 2; attempt++ {
				progress, enabled, err := callJob(context.Background(), client, kind)
				if err == nil || progress != 0 || enabled != nil || calls.Load() != int64(attempt) {
					t.Fatalf("attempt=%d calls=%d progress=%d err=%v", attempt, calls.Load(), progress, err)
				}
			}
		})
	}
}
