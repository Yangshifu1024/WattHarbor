package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/redis/go-redis/v9"
	"wattharbor/internal/platform/config"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/worker/schedule"
)

// deniedOpsRedisHook 统计 Redis 命令并直接返回错误，避免测试建立 Redis 连接。
type deniedOpsRedisHook struct {
	calls atomic.Int64
}

// DialHook 沿用原拨号函数；测试通过命令钩子提前阻止拨号。

func (*deniedOpsRedisHook) DialHook(next redis.DialHook) redis.DialHook { return next }

// ProcessHook 记录真实命令并返回固定错误，不访问外部网络。

func (h *deniedOpsRedisHook) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(context.Context, redis.Cmder) error {
		h.calls.Add(1)
		return errors.New("denied identity reached Redis")
	}
}

// ProcessPipelineHook 统计并阻止批量命令，避免漏掉流水线调用。

func (h *deniedOpsRedisHook) ProcessPipelineHook(redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(_ context.Context, commands []redis.Cmder) error {
		h.calls.Add(int64(len(commands)))
		return errors.New("denied identity reached Redis pipeline")
	}
}

// TestRegisteredWorkerOpsRejectsBeforeRedis 验证实际注册的运维路由在令牌未配置、缺失或错误时只返回一次 401，且不执行 Redis 命令。
func TestRegisteredWorkerOpsRejectsBeforeRedis(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected string
		provided string
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
