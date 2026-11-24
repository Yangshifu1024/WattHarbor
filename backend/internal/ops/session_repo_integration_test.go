//go:build integration

package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/platform/sessiontoken"
)

// TestAdminSessionConcurrentRotationHasOneWinner 验证两个真实 Redis 调用同令牌轮换只有一次成功，旧令牌仍可注销。
// 仅清理本测试创建的会话和 MFA 挑战；验证挑战不能用于登录，版本不匹配时会话无效。
func TestAdminSessionConcurrentRotationHasOneWinner(t *testing.T) {
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Fatal("TEST_REDIS_URL is required")
	}
	options, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := SessionRepo{Redis: client}
	account := operations.AccountIdentity{ID: 18446744073709551557, AuthVersion: 9007199254741111, Username: "admin-concurrent"}
	sid, token, err := repo.Create(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Del(context.Background(), adminTokens.Key(sid))
	start := make(chan struct{})
	results := make(chan error, 2)
	var launched sync.WaitGroup
	launched.Add(2)
	for range 2 {
		go func() {
			launched.Done()
			<-start
			actual, nextSID, _, err := repo.Rotate(ctx, token)
			if err == nil && (actual != account || nextSID != sid) {
				err = fmt.Errorf("identity changed")
			}
			results <- err
		}()
	}
	launched.Wait()
	close(start)
	winners := 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
		} else if !errors.Is(err, operations.ErrInvalidRefresh) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
	if matches, err := repo.Matches(ctx, sid, account.ID, account.AuthVersion+1); err != nil || matches {
		t.Fatalf("wrong version accepted %v %v", matches, err)
	}
	if matches, err := repo.Matches(ctx, sid, account.ID, account.AuthVersion); err != nil || !matches {
		t.Fatalf("exact version lost %v %v", matches, err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, _, _, err := repo.Rotate(canceled, token); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
	if err := repo.Revoke(ctx, token); err != nil {
		t.Fatal(err)
	}
	if exists, err := repo.Exists(ctx, sid); err != nil || exists {
		t.Fatalf("old token revoke %v %v", exists, err)
	}
	challenge, err := repo.BeginMFA(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Del(context.Background(), mfaKey(challenge))
	if ttl, err := client.PTTL(ctx, mfaKey(challenge)).Result(); err != nil || ttl > 5*time.Minute || ttl < 5*time.Minute-5*time.Second {
		t.Fatalf("challenge TTL %v %v", ttl, err)
	}
	if actual, err := repo.ResolveMFA(ctx, challenge); err != nil || actual.Account != account {
		t.Fatalf("challenge identity %+v %v", actual, err)
	}
	if exists, err := repo.Exists(ctx, challenge); err != nil || exists {
		t.Fatalf("challenge became session %v %v", exists, err)
	}
}

// TestAdminSessionLegacyPrecisionSurvivesTwoRotations 验证旧数字 ID／版本在真实 Lua 连续两轮后仍精确并维持绝对 TTL。
// 本测试只写自身随机 SID 并清理，不使用开发 Redis；故障与并发由后续独立用例覆盖。
func TestAdminSessionLegacyPrecisionSurvivesTwoRotations(t *testing.T) {
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Fatal("TEST_REDIS_URL is required")
	}
	options, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sid, token, secret, err := adminTokens.Mint()
	if err != nil {
		t.Fatal(err)
	}
	key := adminTokens.Key(sid)
	defer func() {
		if err := client.Del(context.Background(), key).Err(); err != nil {
			t.Error(err)
		}
	}()
	account := operations.AccountIdentity{ID: 18446744073709551557, AuthVersion: 9007199254741111, Username: "legacy-admin-precision"}
	legacy := fmt.Sprintf(`{"uid":%d,"version":%d,"username":%q,"digest":%q}`, account.ID, account.AuthVersion, account.Username, sessiontoken.Digest(secret))
	if err := client.Set(ctx, key, legacy, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	repo := SessionRepo{Redis: client}
	before, err := client.PTTL(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	for round := range 2 {
		actual, nextSID, next, err := repo.Rotate(ctx, token)
		if err != nil || actual != account || nextSID != sid {
			t.Fatalf("round %d identity %+v err=%v", round, actual, err)
		}
		if matches, err := repo.Matches(ctx, sid, account.ID, account.AuthVersion); err != nil || !matches {
			t.Fatalf("round %d stored precision match=%v err=%v", round, matches, err)
		}
		token = next
	}
	after, err := client.PTTL(ctx, key).Result()
	if err != nil || after > before || after < before-5*time.Second {
		t.Fatalf("TTL %v -> %v err=%v", before, after, err)
	}
	if err := repo.Revoke(ctx, token); err != nil {
		t.Fatal(err)
	}
}

// TestAdminSessionSharedRevokeAndRedisFailure 验证后台轮换和 previous 注销，以及损坏 Redis 记录报错。
// 使用 TEST_REDIS_URL，仅清理本测试 SID。
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
	repo := SessionRepo{Redis: client}
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
