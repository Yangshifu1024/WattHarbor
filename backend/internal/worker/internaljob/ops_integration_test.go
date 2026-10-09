//go:build integration

package internaljob

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// opsIntegrationRedis 为 t 打开 TEST_REDIS_URL 并核对真实 PING；缺配置或连接失败终止，不跳过，结束关闭连接。
func opsIntegrationRedis(t *testing.T) *redis.Client {
	t.Helper()
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Fatal("TEST_REDIS_URL is required")
	}
	options, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close private Redis client: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("connect independent Redis: %v", err)
	}
	return client
}

// TestOpsRegisteredRouteWithRedis 验证实际注册接口读取真实流长度、缺失流零长度以及类型错误的 503；仅写随机私有键。
// 拒绝令牌零 Redis 调用由默认 spy 回归证明；本测试不覆盖业务流消费、死信或共享开发数据。
func TestOpsRegisteredRouteWithRedis(t *testing.T) {
	client := opsIntegrationRedis(t)
	prefix := "ops:route-proof:" + rand.Text()
	streamKey, missingKey, wrongKey := prefix+":events", prefix+":missing", prefix+":wrong"
	keys := []string{streamKey, missingKey, wrongKey}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Del(ctx, keys...).Err(); err != nil {
			t.Errorf("delete private fixture keys: %v", err)
			return
		}
		remaining, err := client.Exists(ctx, keys...).Result()
		if err != nil || remaining != 0 {
			t.Errorf("private fixture cleanup left keys: count=%d error=%v", remaining, err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for _, eventID := range []string{"one", "two"} {
		if err := client.XAdd(ctx, &redis.XAddArgs{Stream: streamKey, Values: map[string]any{"event_id": eventID}}).Err(); err != nil {
			t.Fatalf("seed private stream: %v", err)
		}
	}
	before := time.Now().UTC()
	response := callOpsRoute(t, OpsAPI{Stream: client, Streams: []string{streamKey, missingKey}, ServiceToken: "proof-token"}, "proof-token", ctx)
	after := time.Now().UTC()
	reply := decodeOpsReply(t, response)
	if response.Code != http.StatusOK || reply.Code != 0 || reply.Message != "ok" {
		t.Fatalf("real stream response: status=%d reply=%+v", response.Code, reply)
	}
	var data opsStreamsReply
	if err := json.Unmarshal(reply.Data, &data); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data.Pending, map[string]int64{streamKey: 2, missingKey: 0}) || data.CheckedAt.Before(before) || data.CheckedAt.After(after) {
		t.Fatalf("real lengths or query timestamp changed: %+v", data)
	}
	if err := client.Set(ctx, wrongKey, "private-string", 0).Err(); err != nil {
		t.Fatalf("seed wrong Redis type: %v", err)
	}
	response = callOpsRoute(t, OpsAPI{Stream: client, Streams: []string{wrongKey}, ServiceToken: "proof-token"}, "proof-token", ctx)
	// 在解码前保存完整响应，避免解码器消费记录器后丢失泄露检查证据。
	body := response.Body.String()
	reply = decodeOpsReply(t, response)
	if response.Code != http.StatusServiceUnavailable || reply.Code != 5003 || reply.Message != "stream 状态暂时不可读" || string(reply.Data) != "null" {
		t.Fatalf("wrong type response: status=%d reply=%+v", response.Code, reply)
	}
	if strings.Contains(body, "WRONGTYPE") || strings.Contains(body, wrongKey) || strings.Contains(body, "private-string") {
		t.Fatalf("internal Redis error leaked: %s", body)
	}
}
