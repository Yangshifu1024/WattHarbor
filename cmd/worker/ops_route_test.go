package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/schedule"
	"github.com/redis/go-redis/v9"
)

// deniedOpsRedisHook 在实际客户端命令入口计数并阻断网络，证明拒绝身份不会进入 Redis。
type deniedOpsRedisHook struct {
	calls atomic.Int64 // 当前请求生命周期内实际命令次数。
}

// DialHook 保留客户端拨号能力；本验收只允许 ProcessHook 在拨号前阻断。
func (*deniedOpsRedisHook) DialHook(next redis.DialHook) redis.DialHook { return next }

// ProcessHook 记录真实命令并返回固定错误，不访问外部网络。
func (h *deniedOpsRedisHook) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(context.Context, redis.Cmder) error {
		h.calls.Add(1)
		return errors.New("denied identity reached Redis")
	}
}

// ProcessPipelineHook 同样阻断并统计流水线，不能通过换入口绕过零命令断言。
func (h *deniedOpsRedisHook) ProcessPipelineHook(redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(_ context.Context, commands []redis.Cmder) error {
		h.calls.Add(int64(len(commands)))
		return errors.New("denied identity reached Redis pipeline")
	}
}

// TestRegisteredWorkerOpsRejectsBeforeRedis 核对实际 cmd 注册含非空业务流的运维端点。
// 未配置、缺失和错误身份只写一个401信封且无 Redis 命令，不以 nil 依赖未崩溃冒充准入证据。
func TestRegisteredWorkerOpsRejectsBeforeRedis(t *testing.T) {
	for _, tc := range []struct {
		name     string // 当前拒绝场景。
		expected string // 实际配置令牌，空配置仍须拒绝。
		provided string // 当前HTTP提供的令牌，不自动 trim。
	}{
		{"unconfigured", "", ""},
		{"unconfigured with header", "", "worker-ops"},
		{"missing", "worker-ops", ""},
		{"wrong", "worker-ops", "other"},
		{"whitespace mismatch", "worker-ops", "worker-ops "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
			t.Cleanup(func() { _ = stream.Close() })
			hook := &deniedOpsRedisHook{}
			stream.AddHook(hook)
			router := newRouter(config.Worker{ServiceToken: tc.expected}, &resources{Stream: stream}, schedule.Scheduler{})
			r := httptest.NewRequest(http.MethodGet, "/api/v1/internal/worker/ops/streams", nil)
			r.Header.Set("X-Service-Token", tc.provided)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			var reply httpapi.Envelope
			decoder := json.NewDecoder(w.Body)
			if err := decoder.Decode(&reply); err != nil || w.Code != http.StatusUnauthorized || reply.Code != httpapi.CodeUnauthorized || reply.Data != nil {
				t.Fatalf("registered denial=%d %+v: %v", w.Code, reply, err)
			}
			if err := decoder.Decode(&reply); !errors.Is(err, io.EOF) || hook.calls.Load() != 0 {
				t.Fatalf("denial continued: Redis commands=%d second JSON=%v", hook.calls.Load(), err)
			}
		})
	}
}
