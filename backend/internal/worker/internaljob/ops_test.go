package internaljob

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"wattharbor/internal/platform/httpapi"
)

// opsRedisSpy 记录 XLen 的流名和 context，返回预设长度或错误。
type opsRedisSpy struct {
	// UniversalClient 仅满足已有客户端接口，未覆盖的方法不可调用。
	redis.UniversalClient
	// lengths 为每个流的固定长度，不区分消费状态。
	lengths map[string]int64
	// failures 为指定流返回的错误。
	failures map[string]error
	calls    []string
	// contexts 保存传入 XLen 的 context，用于核对请求取消传播。
	contexts []context.Context
}

// XLen 记录请求，返回预设流长度、错误或 context 取消错误。
func (s *opsRedisSpy) XLen(ctx context.Context, name string) *redis.IntCmd {
	s.calls = append(s.calls, name)
	s.contexts = append(s.contexts, ctx)
	cmd := redis.NewIntCmd(ctx)
	if err := ctx.Err(); err != nil {
		cmd.SetErr(err)
	} else if err := s.failures[name]; err != nil {
		cmd.SetErr(err)
	} else {
		cmd.SetVal(s.lengths[name])
	}
	return cmd
}

// opsEnvelopeReply 保存 HTTP 响应的业务码、消息和数据。
type opsEnvelopeReply struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// opsStreamsReply 保存流长度和查询时刻，Pending 表示长度而非未处理消息数。
type opsStreamsReply struct {
	// Pending 按流名保存 XLen 返回值。
	Pending map[string]int64 `json:"pending"`
	// CheckedAt 为处理器生成的 UTC 查询时间。
	CheckedAt time.Time `json:"checked_at"`
}

// callOpsRoute 注册接口并携令牌请求路径，返回响应记录器。
func callOpsRoute(t *testing.T, api OpsAPI, token string, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	router := httpapi.NewRouter()
	api.Register(router)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/worker/ops/streams", nil).WithContext(ctx)
	if token != "" {
		req.Header.Set("X-Service-Token", token)
	}
	reply := httptest.NewRecorder()
	router.ServeHTTP(reply, req)
	return reply
}

// decodeOpsReply 为 t 解码记录器的唯一响应；额外 JSON 或解码失败终止，防止重复写响应漏检。
func decodeOpsReply(t *testing.T, response *httptest.ResponseRecorder) opsEnvelopeReply {
	t.Helper()
	decoder := json.NewDecoder(response.Body)
	var reply opsEnvelopeReply
	if err := decoder.Decode(&reply); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Errorf("response must contain one JSON envelope, extra=%v error=%v", extra, err)
	}
	return reply
}

func TestOpsRouteRejectsBeforeRedis(t *testing.T) {
	cases := []struct {
		name string
		// configured 为服务配置令牌。
		configured string
		// provided 为请求携带的原始令牌。
		provided string
	}{
		{name: "unconfigured"},
		{name: "unconfigured_with_header", provided: "secret"},
		{name: "missing_header", configured: "secret"},
		{name: "wrong_same_length", configured: "secret", provided: "wrong!"},
		{name: "wrong_other_length", configured: "secret", provided: "wrong"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &opsRedisSpy{lengths: map[string]int64{"private": 2}}
			response := callOpsRoute(t, OpsAPI{Stream: spy, Streams: []string{"private"}, ServiceToken: tc.configured}, tc.provided, t.Context())
			if len(spy.calls) != 0 {
				t.Errorf("rejected request accessed Redis: %v", spy.calls)
			}
			reply := decodeOpsReply(t, response)
			if response.Code != http.StatusUnauthorized || reply.Code != httpapi.CodeUnauthorized || reply.Message != "service token invalid" || string(reply.Data) != "null" {
				t.Fatalf("unexpected rejection: status=%d reply=%+v", response.Code, reply)
			}
		})
	}
}

func TestOpsRouteReportsStreamLengths(t *testing.T) {
	spy := &opsRedisSpy{lengths: map[string]int64{"one": 2, "two": 7}, failures: map[string]error{"nil": redis.Nil}}
	ctx := t.Context()
	before := time.Now().UTC()
	response := callOpsRoute(t, OpsAPI{Stream: spy, Streams: []string{"one", "nil", "two"}, ServiceToken: " secret "}, " secret ", ctx)
	after := time.Now().UTC()
	reply := decodeOpsReply(t, response)
	if response.Code != http.StatusOK || reply.Code != 0 || reply.Message != "ok" {
		t.Fatalf("unexpected success: status=%d reply=%+v", response.Code, reply)
	}
	var data opsStreamsReply
	if err := json.Unmarshal(reply.Data, &data); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data.Pending, map[string]int64{"one": 2, "two": 7}) || !reflect.DeepEqual(spy.calls, []string{"one", "nil", "two"}) {
		t.Fatalf("lengths=%v calls=%v", data.Pending, spy.calls)
	}
	if data.CheckedAt.Before(before) || data.CheckedAt.After(after) || data.CheckedAt.Location() != time.UTC {
		t.Fatalf("checked_at outside UTC request interval: %s", reply.Data)
	}
	for _, forwarded := range spy.contexts {
		if forwarded != ctx {
			t.Fatal("XLen did not receive original request context")
		}
	}
}

func TestOpsRouteRedisFailureAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "redis_failure"
		if canceled {
			name = "canceled_request"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			spy := &opsRedisSpy{failures: map[string]error{"first": errors.New("WRONGTYPE private-detail")}}
			if canceled {
				cancel()
			}
			response := callOpsRoute(t, OpsAPI{Stream: spy, Streams: []string{"first", "later"}, ServiceToken: "secret"}, "secret", ctx)
			reply := decodeOpsReply(t, response)
			if response.Code != http.StatusServiceUnavailable || reply.Code != 5003 || reply.Message != "stream 状态暂时不可读" || string(reply.Data) != "null" {
				t.Fatalf("unexpected failure: status=%d reply=%+v", response.Code, reply)
			}
			if !reflect.DeepEqual(spy.calls, []string{"first"}) || len(spy.contexts) != 1 || spy.contexts[0] != ctx {
				t.Fatalf("failure did not stop reads or propagate context: %v", spy.calls)
			}
			if canceled && !errors.Is(spy.contexts[0].Err(), context.Canceled) {
				t.Fatal("cancellation was not propagated")
			}
		})
	}
}
