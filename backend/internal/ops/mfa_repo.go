package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"time"
)

// mfaCompareDelete 比较读取的值后删除 Redis 键，用于挑战、待绑定记录和租锁。
var mfaCompareDelete = redis.NewScript(`if redis.call('GET',KEYS[1]) == ARGV[1] then return redis.call('DEL',KEYS[1]) end; return 0`)

// mfaEnrollment 是短期 MFA 设置记录，不直接返回 HTTP 或写入审计。
type mfaEnrollment struct {
	ID          string    `json:"id"`           // 绑定标识
	Secret      string    `json:"secret"`       // 服务端生成的待确认密钥
	AdminID     uint64    `json:"admin_id"`     // 后台账号 ID，按整数解码
	AuthVersion uint64    `json:"auth_version"` // 认证版本，确认时必须一致
	SessionID   string    `json:"session_id"`   // 发起绑定的会话 SID，确认时必须一致
	ExpiresAt   time.Time `json:"expires_at"`   // 绝对到期时刻
}

// MFARepo 保存设置记录、租锁和次数，验证规则由 MFAService 决定。
type MFARepo struct {
	Redis *redis.Client /* cmd 拥有的安全缓存 */
}

// mfaPendingKey 构造账号待绑定记录键。
func mfaPendingKey(id uint64) string { return fmt.Sprintf("admin:mfa-settings:pending:%d", id) }

// mfaLockKey 构造账号设置租锁键。
func mfaLockKey(id uint64) string { return fmt.Sprintf("admin:mfa-settings:lock:%d", id) }

// mfaSettingsRateKey 构造账号安全验证计数键。
func mfaSettingsRateKey(id uint64) string { return fmt.Sprintf("admin:mfa-settings:rate:%d", id) }

// Count 增加账号设置次数，保持首次一分钟窗口。
func (r MFARepo) Count(ctx context.Context, id uint64) (int, error) {
	return authRateScript.Run(ctx, r.Redis, []string{mfaSettingsRateKey(id)}).Int()
}

// Acquire 用指定 owner 取得账号租锁，返回是否取得。
func (r MFARepo) Acquire(ctx context.Context, id uint64, owner string, ttl time.Duration) (bool, error) {
	return r.Redis.SetNX(ctx, mfaLockKey(id), owner, ttl).Result()
}

// Release 只释放仍属于指定 owner 的租锁，不能删除后续持有者的锁。
func (r MFARepo) Release(ctx context.Context, id uint64, owner string) error {
	return mfaCompareDelete.Run(ctx, r.Redis, []string{mfaLockKey(id)}, owner).Err()
}

// SavePending 编码待绑定记录并按 TTL 保存。
func (r MFARepo) SavePending(ctx context.Context, id uint64, pending mfaEnrollment, ttl time.Duration) error {
	raw, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	return r.Redis.Set(ctx, mfaPendingKey(id), raw, ttl).Err()
}

// Pending 读取挑战和用于比较的原始文本，不删除挑战。
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

// ConsumePending 仅在挑战仍等于原始文本时删除，返回是否删除成功。
func (r MFARepo) ConsumePending(ctx context.Context, id uint64, raw string) (bool, error) {
	n, err := mfaCompareDelete.Run(ctx, r.Redis, []string{mfaPendingKey(id)}, raw).Int()
	return n == 1, err
}

// ClearPending 删除账号待绑定记录，不改变 SQL 中的 MFA 状态。
func (r MFARepo) ClearPending(ctx context.Context, id uint64) error {
	return r.Redis.Del(ctx, mfaPendingKey(id)).Err()
}
