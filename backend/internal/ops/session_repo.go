package ops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/sessiontoken"
	"github.com/redis/go-redis/v9"
)

// refreshTTL 定义会话的 7 天绝对寿命；刷新轮换使用 KEEPTTL，不延长过期时间。
const refreshTTL = 7 * 24 * time.Hour

// adminSessionRecord 是 Redis 中以 JSON 保存的会话或挑战记录。
// 刷新令牌仅保存摘要，不保存明文。
type adminSessionRecord struct {
	AuthVersion sessiontoken.JSONUint64 `json:"version"`            // 账号的权限版本号，改密码/改角色后会变，用于让旧会话立刻失效
	AdminID     sessiontoken.JSONUint64 `json:"uid"`                // 后台账号 id
	Username    string                  `json:"username"`           // 登录名，供审计记录操作人
	Digest      string                  `json:"digest"`             // 当前刷新令牌密钥的 sha256 散列，校验用
	Previous    []string                `json:"previous,omitempty"` // 最近 32 个已轮换掉的旧密钥散列，仅允许旧令牌注销，不允许旧令牌再次轮换
}

// SessionRepo 通过 Redis 的 admin:session:<sid> 键管理后台会话。
type SessionRepo struct {
	Redis *redis.Client /* 进程拥有的后台会话连接 */
}

// Create 创建会话并返回 ART_<sid>.<secret> 刷新令牌，完整明文只在此返回。
func (s SessionRepo) Create(ctx context.Context, account operations.AccountIdentity) (string, string, error) {
	sid, token, secret, err := adminTokens.Mint()
	if err != nil {
		return "", "", err
	}
	record, err := json.Marshal(adminSessionRecord{AuthVersion: sessiontoken.JSONUint64(account.AuthVersion), AdminID: sessiontoken.JSONUint64(account.ID), Username: account.Username, Digest: sessiontoken.Digest(secret)})
	if err != nil {
		return "", "", err
	}
	if err := s.Redis.Set(ctx, adminTokens.Key(sid), record, refreshTTL).Err(); err != nil {
		return "", "", err
	}
	return sid, token, nil
}

// Rotate 比较当前摘要后轮换刷新令牌，摘要不匹配时返回 ErrInvalidRefresh；旧令牌仍可用于注销。
// 旧 JSON 的数字 ID 和版本保持精度，连续轮换不延长绝对 TTL。
func (s SessionRepo) Rotate(ctx context.Context, token string) (operations.AccountIdentity, string, string, error) {
	sid, oldSecret, ok := adminTokens.Parse(token)
	if !ok {
		return operations.AccountIdentity{}, "", "", operations.ErrInvalidRefresh
	}
	newSecret, err := auth.RandomSecret(32)
	if err != nil {
		return operations.AccountIdentity{}, "", "", err
	}
	result, err := sessiontoken.RotateScript.Run(ctx, s.Redis, []string{adminTokens.Key(sid)}, sessiontoken.Digest(oldSecret), sessiontoken.Digest(newSecret), "uid", "version").Result()
	if errors.Is(err, redis.Nil) {
		return operations.AccountIdentity{}, "", "", operations.ErrInvalidRefresh
	}
	if err != nil {
		return operations.AccountIdentity{}, "", "", err
	}
	if result == nil || result == int64(0) {
		return operations.AccountIdentity{}, "", "", operations.ErrInvalidRefresh
	}
	value, ok := result.(string)
	if !ok {
		return operations.AccountIdentity{}, "", "", operations.ErrInvalidRefresh
	}
	var record adminSessionRecord
	if err := json.Unmarshal([]byte(value), &record); err != nil {
		return operations.AccountIdentity{}, "", "", err
	}
	identity := operations.AccountIdentity{ID: uint64(record.AdminID), Username: record.Username, AuthVersion: uint64(record.AuthVersion)}
	return identity, sid, adminTokens.TokenPrefix + sid + "." + newSecret, nil
}

// Revoke 比对当前或 previous 刷新摘要后原子删除会话。
func (s SessionRepo) Revoke(ctx context.Context, token string) error {
	sid, secret, ok := adminTokens.Parse(token)
	if !ok {
		return operations.ErrInvalidRefresh
	}
	result, err := sessiontoken.RevokeScript.Run(ctx, s.Redis, []string{adminTokens.Key(sid)}, sessiontoken.Digest(secret)).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return operations.ErrInvalidRefresh
	}
	return nil
}

// Exists 检查 SID 会话，空 SID 返回 false。
func (s SessionRepo) Exists(ctx context.Context, sid string) (bool, error) {
	if sid == "" {
		return false, nil
	}
	n, err := s.Redis.Exists(ctx, adminTokens.Key(sid)).Result()
	return n == 1, err
}

// Matches 核对会话账号和认证版本，精确读取新字符串及旧数字记录。
// 改密或角色变化提升 AuthVersion，旧会话随即失效。
func (s SessionRepo) Matches(ctx context.Context, sid string, id, version uint64) (bool, error) {
	if sid == "" {
		return false, nil
	}
	value, err := s.Redis.Get(ctx, adminTokens.Key(sid)).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var record adminSessionRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return false, err
	}
	return uint64(record.AdminID) == id && uint64(record.AuthVersion) == version, nil
}

// RevokeSession 删除已鉴权 SID 的会话，使其访问令牌失效，无需再次校验刷新摘要。
func (s SessionRepo) RevokeSession(ctx context.Context, sid string) error {
	if sid == "" {
		return operations.ErrInvalidRefresh
	}
	return s.Redis.Del(ctx, adminTokens.Key(sid)).Err()
}

// mfaChallengeTTL 限制密码验证通过后完成二次验证的时间窗口。
const mfaChallengeTTL = 5 * time.Minute

// BeginMFA 保存五分钟挑战，仅包含账号和版本，不含密码或验证码，也不能作为 API 会话。
func (s SessionRepo) BeginMFA(ctx context.Context, account operations.AccountIdentity) (string, error) {
	token, err := auth.RandomSecret(32)
	if err != nil {
		return "", err
	}
	record, err := json.Marshal(adminSessionRecord{AuthVersion: sessiontoken.JSONUint64(account.AuthVersion), AdminID: sessiontoken.JSONUint64(account.ID), Username: account.Username})
	if err != nil {
		return "", err
	}
	if err := s.Redis.Set(ctx, mfaKey(token), record, mfaChallengeTTL).Err(); err != nil {
		return "", err
	}
	return token, nil
}

// MFAChallenge 保存从存储读出的账号和读取时的完整文本，不向 HTTP 暴露。
type MFAChallenge struct {
	Account operations.AccountIdentity // 密码验证时的账号及权限版本
	token   string                     // 随机挑战，决定唯一存储键
	raw     string                     // JSON 文本，用于拒绝并发替换／重复删除
}

// ResolveMFA 读取挑战及原始文本，不删除记录；坏码可用同一挑战重试。
func (s SessionRepo) ResolveMFA(ctx context.Context, token string) (MFAChallenge, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return MFAChallenge{}, operations.ErrInvalidMFAChallenge
	}
	value, err := s.Redis.Get(ctx, mfaKey(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return MFAChallenge{}, operations.ErrInvalidMFAChallenge
	}
	if err != nil {
		return MFAChallenge{}, err
	}
	var record adminSessionRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return MFAChallenge{}, operations.ErrInvalidMFAChallenge
	}
	if record.AdminID == 0 {
		return MFAChallenge{}, operations.ErrInvalidMFAChallenge
	}
	return MFAChallenge{Account: operations.AccountIdentity{ID: uint64(record.AdminID), AuthVersion: uint64(record.AuthVersion), Username: record.Username}, token: token, raw: string(value)}, nil
}

// ConsumeMFA 比较原始 JSON 并原子删除挑战，缺失或已替换时拒绝；Redis 故障不能当作登录成功。
func (s SessionRepo) ConsumeMFA(ctx context.Context, challenge MFAChallenge) error {
	if challenge.token == "" || challenge.raw == "" {
		return operations.ErrInvalidMFAChallenge
	}
	n, err := mfaCompareDelete.Run(ctx, s.Redis, []string{mfaKey(challenge.token)}, challenge.raw).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return operations.ErrInvalidMFAChallenge
	}
	return nil
}

// mfaKey 构造独立于会话的挑战 Redis 键，避免两类记录混用。
func mfaKey(token string) string { return "admin:mfa:" + token }

// adminTokens 保留后台专属前缀，通用编码与摘要由 platform 提供。
var adminTokens = sessiontoken.Layout{TokenPrefix: "ART_", KeyPrefix: "admin:session:"}
