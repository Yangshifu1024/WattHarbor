package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"time"
)

// mfaCompareDelete 仅删除仍等于原值的键；登录挑战、设置凭据和租锁共用CAS机制。
var mfaCompareDelete = redis.NewScript(`if redis.call('GET',KEYS[1]) == ARGV[1] then return redis.call('DEL',KEYS[1]) end; return 0`)

// mfaEnrollment 是所属缓存中的短期设置凭据，不直接返回HTTP或审计。
type mfaEnrollment struct {
	ID          string    `json:"id"`           // 原绑定标识
	Secret      string    `json:"secret"`       // 服务端生成的待确认密钥
	AdminID     uint64    `json:"admin_id"`     // 原账号身份，Go按整数精确解码
	AuthVersion uint64    `json:"auth_version"` // 原认证版本，确认时必须一致
	SessionID   string    `json:"session_id"`   // 只能在原会话确认
	ExpiresAt   time.Time `json:"expires_at"`   // 绝对到期时刻
}

// MFARepo 提供设置凭据、租锁与频次计数IO；认证规则由MFAService决定。
type MFARepo struct {
	Redis *redis.Client /* cmd拥有的安全缓存 */
}

// mfaPendingKey 返回id账号的原短期绑定键，无IO。
func mfaPendingKey(id uint64) string { return fmt.Sprintf("admin:mfa-settings:pending:%d", id) }

// mfaLockKey 返回id账号的原设置租锁键，无IO。
func mfaLockKey(id uint64) string { return fmt.Sprintf("admin:mfa-settings:lock:%d", id) }

// mfaSettingsRateKey 返回id账号的原安全验证计数键，无IO。
func mfaSettingsRateKey(id uint64) string { return fmt.Sprintf("admin:mfa-settings:rate:%d", id) }

// Count 在ctx下增加id原设置计数并保留一分钟窗口，返回计数或Redis错误。
func (r MFARepo) Count(ctx context.Context, id uint64) (int, error) {
	return authRateScript.Run(ctx, r.Redis, []string{mfaSettingsRateKey(id)}).Int()
}

// Acquire 在ctx下用owner抢占id的ttl租锁，返回获胜事实或Redis错误。
func (r MFARepo) Acquire(ctx context.Context, id uint64, owner string, ttl time.Duration) (bool, error) {
	return r.Redis.SetNX(ctx, mfaLockKey(id), owner, ttl).Result()
}

// Release 在ctx下仅释放id仍属于owner的租锁，返回Redis错误，不删除后继持有者。
func (r MFARepo) Release(ctx context.Context, id uint64, owner string) error {
	return mfaCompareDelete.Run(ctx, r.Redis, []string{mfaLockKey(id)}, owner).Err()
}

// SavePending 在ctx下编码pending并以ttl替换id的短期凭据，返回编码／Redis错误。
func (r MFARepo) SavePending(ctx context.Context, id uint64, pending mfaEnrollment, ttl time.Duration) error {
	raw, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	return r.Redis.Set(ctx, mfaPendingKey(id), raw, ttl).Err()
}

// Pending 在ctx下读取id的原凭据及不透明CAS文本，返回缺失／坏记录／存储错误；不消费。
func (r MFARepo) Pending(ctx context.Context, id uint64) (mfaEnrollment, string, error) {
	raw, err := r.Redis.Get(ctx, mfaPendingKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return mfaEnrollment{}, "", ErrMFAPending
	}
	if err != nil {
		return mfaEnrollment{}, "", err
	}
	var pending mfaEnrollment
	if err := json.Unmarshal(raw, &pending); err != nil {
		return mfaEnrollment{}, "", ErrMFAPending
	}
	return pending, string(raw), nil
}

// ConsumePending 在ctx下仅消费id仍等于raw原文本的凭据，返回获胜事实或Redis错误。
func (r MFARepo) ConsumePending(ctx context.Context, id uint64, raw string) (bool, error) {
	n, err := mfaCompareDelete.Run(ctx, r.Redis, []string{mfaPendingKey(id)}, raw).Int()
	return n == 1, err
}

// ClearPending 在ctx下删除id原待绑定记录，返回Redis错误，不改SQL安全状态。
func (r MFARepo) ClearPending(ctx context.Context, id uint64) error {
	return r.Redis.Del(ctx, mfaPendingKey(id)).Err()
}
