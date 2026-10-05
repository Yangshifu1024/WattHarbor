//go:build integration

package redis

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/redis/go-redis/v9"
)

// TestAdminSessionSharedRevokeAndRedisFailure 验证提取通用撤销后后台原轮换与 previous 注销契约。
// Redis 为真实 TEST_REDIS_URL；损坏记录产生存储错误，测试只删除本次 SID，不覆盖待迁移的后台用例验收。
func TestAdminSessionSharedRevokeAndRedisFailure(t *testing.T) {
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Fatal("TEST_REDIS_URL is required")
	}
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	repo := AdminSessions{Redis: client}
	account := operations.AccountIdentity{ID: 42, AuthVersion: 3, Username: "generic-script-fixture"}
	sid, token, err := repo.Create(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Del(context.Background(), adminTokens.Key(sid)).Err(); err != nil {
			t.Error(err)
		}
	})
	before, err := client.PTTL(ctx, adminTokens.Key(sid)).Result()
	if err != nil {
		t.Fatal(err)
	}
	actual, nextSID, _, err := repo.Rotate(ctx, token)
	if err != nil || actual != account || nextSID != sid {
		t.Fatalf("admin original identity=%+v err=%v", actual, err)
	}
	after, err := client.PTTL(ctx, adminTokens.Key(sid)).Result()
	if err != nil || after > before || after < before-5*time.Second {
		t.Fatalf("admin absolute TTL=%v -> %v err=%v", before, after, err)
	}
	if err := repo.Revoke(ctx, token); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.Exists(ctx, sid); err != nil || ok {
		t.Fatal("previous token did not revoke admin session")
	}
	sid, token, err = repo.Create(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, adminTokens.Key(sid), "{invalid-json", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.Rotate(ctx, token); err == nil || errors.Is(err, operations.ErrInvalidRefresh) {
		t.Fatalf("admin storage error misclassified=%v", err)
	}
}
