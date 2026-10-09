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

	"chargepilot/internal/platform/httpapi"
	"github.com/redis/go-redis/v9"
)

// opsRedisSpy 仅替代 XLen，记录真实注册路由的命令和请求 context；不连接 Redis。
type opsRedisSpy struct {
	// UniversalClient 仅满足已有客户端接口，未覆盖的方法不可调用。
	redis.UniversalClient
	// lengths 为每个流的固定长度，不区分消费状态。
	lengths map[string]int64
	// failures 为指定流返回的错误。
	failures map[string]error
	// calls 按请求执行顺序记录流名。
	calls []string
	// contexts 保存传入 XLen 的 context，用于核对请求取消传播。
	contexts []context.Context
}

// XLen 接收请求 ctx 和流名，记录调用后返回固定长度、指定错误或取消错误，无网络副作用。
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

// opsEnvelopeReply 读取实际 HTTP 信封，只用于核对响应边界。
type opsEnvelopeReply struct {
	// Code 为既有业务状态码。
	Code int `json:"code"`
	// Message 为既有公开消息。
	Message string `json:"message"`
	// Data 保留 JSON 数据，失败时应为 null。
	Data json.RawMessage `json:"data"`
}

// opsStreamsReply 读取实际成功响应；Pending 是流长度而非未消费数。
type opsStreamsReply struct {
	// Pending 按流名保存 XLen 返回值。
	Pending map[string]int64 `json:"pending"`
	// CheckedAt 为处理器生成的 UTC 查询时间。
	CheckedAt time.Time `json:"checked_at"`
}

// callOpsRoute 为 t 实际注册 api 并以 ctx 和 token 请求路径，返回记录器。
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

// TestOpsRouteRejectsBeforeRedis 以真实 Register 验证拒绝身份仅返回一次 401 且不进入 Redis。
func TestOpsRouteRejectsBeforeRedis(t *testing.T) {
	cases := []struct {
		// name 标识不同拒绝边界。
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

// TestOpsRouteReportsStreamLengths 核对真实成功响应、令牌精确比较、redis.Nil 跳过和 context 传递；不宣称真实 Redis 语义。
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

// TestOpsRouteRedisFailureAndCancellation 核对 503 映射、隐藏内部错误、失败后停止读取以及取消 context 传播，无真实 Redis IO。
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
