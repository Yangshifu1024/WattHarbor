//go:build integration

package identity

import (
	"context"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"os"
	"sync"
	"testing"
	"time"
	"wattharbor/internal/platform/sessiontoken"
)

// identityRedis 为 t 打开独立 TEST_REDIS_URL 并核对真实 PING，缺配置或连接失败终止，不跳过。
func identityRedis(t *testing.T) *redis.Client {
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
	t.Cleanup(func() { client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	return client
}

// TestSessionRepoAtomicRotationAndPreviousRevocation 使用真实 Redis 同时刷新令牌，验证仅一次获胜与 KEEPTTL。
// 旧摘要可注销会话；测试仅清理自己随机 SID，不清空其他会话。
func TestSessionRepoAtomicRotationAndPreviousRevocation(t *testing.T) {
	client := identityRedis(t)
	repo := SessionRepo{Redis: client}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	user := User{ID: 923456789012345678, OpenID: "identity-redis-fixture"}
	sid, token, err := repo.Create(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	key := userTokens.Key(sid)
	t.Cleanup(func() {
		if err := client.Del(context.Background(), key).Err(); err != nil {
			t.Error(err)
		}
	})
	before, err := client.PTTL(ctx, key).Result()
	if err != nil || before <= 0 {
		t.Fatalf("initial TTL=%v %v", before, err)
	}
	type result struct {
		user       User   /* 用户 ID */
		sid, token string /* SID及新令牌 */
		err        error  /* 轮换结果 */
	}
	start := make(chan struct{})
	done := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			u, s, next, err := repo.Rotate(ctx, token)
			done <- result{u, s, next, err}
		}()
	}
	ready.Wait()
	close(start)
	won, lost := 0, 0
	for range 2 {
		select {
		case r := <-done:
			if r.err == nil {
				won++
				if r.user.ID != user.ID || r.user.OpenID != user.OpenID || r.sid != sid || r.token == token {
					t.Fatalf("original identity changed uid=%d sid_matches=%v", r.user.ID, r.sid == sid)
				}
			} else if errors.Is(r.err, ErrInvalidRefresh) {
				lost++
			} else {
				t.Fatal(r.err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("rotation won=%d lost=%d", won, lost)
	}
	after, err := client.PTTL(ctx, key).Result()
	if err != nil || after > before || after < before-5*time.Second {
		t.Fatalf("absolute TTL changed %v -> %v %v", before, after, err)
	}
	if err := repo.Revoke(ctx, token); err != nil {
		t.Fatalf("previous token cannot revoke %v", err)
	}
	if exists, err := repo.Exists(ctx, sid); err != nil || exists {
		t.Fatalf("revoked session exists=%v %v", exists, err)
	}
	if _, _, _, err := repo.Rotate(ctx, token); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("revoked rotate=%v", err)
	}
}

// TestSessionRepoLegacyNumericIdentitySurvivesLuaRotation 验证 Lua 使用 JSON 数字文本恢复精确旧用户 ID。
// 外来同SID密钥不得撤销记录；取消不得产生新会话，测试仅删除自己键。
func TestSessionRepoLegacyNumericIdentitySurvivesLuaRotation(t *testing.T) {
	client := identityRedis(t)
	repo := SessionRepo{Redis: client}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sid, token, secret, err := userTokens.Mint()
	if err != nil {
		t.Fatal(err)
	}
	key := userTokens.Key(sid)
	t.Cleanup(func() {
		if err := client.Del(context.Background(), key).Err(); err != nil {
			t.Error(err)
		}
	})
	const id = uint64(923456789012345678)
	raw := fmt.Sprintf("{\"uid\":%d,\"openid\":\"legacy\",\"digest\":\"%s\"}", id, sessiontoken.Digest(secret))
	if err := client.Set(ctx, key, raw, time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	_, _, foreign, err := userTokens.Mint()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Revoke(ctx, "RT_"+sid+"."+foreign); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("foreign revoke=%v", err)
	}
	user, nextSID, next, err := repo.Rotate(ctx, token)
	if err != nil || user.ID != id || nextSID != sid || user.OpenID != "legacy" {
		t.Fatalf("legacy precision=%+v %v", user, err)
	}
	user, nextSID, next, err = repo.Rotate(ctx, next)
	if err != nil || user.ID != id || nextSID != sid || user.OpenID != "legacy" {
		t.Fatalf("persisted legacy precision=%+v %v", user, err)
	}
	if err := repo.Revoke(ctx, next); err != nil {
		t.Fatal(err)
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, _, err := repo.Create(cancelled, user); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled create=%v", err)
	}
}

// TestSessionRepoKeepsRedisFailureDistinctFromInvalidRefresh 验证损坏记录返回 Redis 错误，不能伪装为令牌失效。
// 键保留供排查，测试只清理自己的随机 SID。
func TestSessionRepoKeepsRedisFailureDistinctFromInvalidRefresh(t *testing.T) {
	client := identityRedis(t)
	repo := SessionRepo{Redis: client}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sid, token, err := repo.Create(ctx, User{ID: 42, OpenID: "storage-error-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	key := userTokens.Key(sid)
	t.Cleanup(func() {
		if err := client.Del(context.Background(), key).Err(); err != nil {
			t.Error(err)
		}
	})
	if err := client.Set(ctx, key, "{invalid-json", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.Rotate(ctx, token); err == nil || errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("Redis failure disguised as invalid refresh: %v", err)
	}
	if exists, err := repo.Exists(ctx, sid); err != nil || !exists {
		t.Fatalf("failed rotation changed session exists=%v %v", exists, err)
	}
}
