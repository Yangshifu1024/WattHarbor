package redis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/sessiontoken"
	"github.com/redis/go-redis/v9"
)

// refreshTTL 定义会话的 7 天绝对寿命；刷新轮换使用 KEEPTTL，不延长过期时间。
const refreshTTL = 7 * 24 * time.Hour

// adminSessionRecord 是 Redis 中以 JSON 保存的会话或挑战记录。
// 刷新令牌仅保存摘要，不保存明文。
type adminSessionRecord struct {
	AuthVersion uint64   `json:"version"`            // 账号的权限版本号，改密码/改角色后会变，用于让旧会话立刻失效
	AdminID     uint64   `json:"uid"`                // 后台账号 id
	Username    string   `json:"username"`           // 登录名，供审计记录操作人
	Digest      string   `json:"digest"`             // 当前刷新令牌密钥的 sha256 散列，校验用
	Previous    []string `json:"previous,omitempty"` // 最近 32 个已轮换掉的旧密钥散列，只为让上一枚令牌还能用一次
}

// AdminSessions 管理后台会话，底层就是 Redis 里的 admin:session:<sid> 键。
// 结构体只持有 Redis 客户端（字段 Redis），没有本地状态，可以直接按值复制。
type AdminSessions struct{ Redis *redis.Client }

// Create 为一个账号新建会话，返回会话 id（sid）和完整的刷新令牌。
// 令牌形如 ART_<sid>.<secret>，完整明文只在这里返回一次，之后客户端凭它换新令牌。
func (s AdminSessions) Create(ctx context.Context, account operations.AccountIdentity) (string, string, error) {
	sid, token, secret, err := adminTokens.Mint()
	if err != nil {
		return "", "", err
	}
	record, err := json.Marshal(adminSessionRecord{AuthVersion: account.AuthVersion, AdminID: account.ID, Username: account.Username, Digest: sessiontoken.Digest(secret)})
	if err != nil {
		return "", "", err
	}
	if err := s.Redis.Set(ctx, adminTokens.Key(sid), record, refreshTTL).Err(); err != nil {
		return "", "", err
	}
	return sid, token, nil
}

// adminRotateScript 原子校验并轮换刷新令牌摘要，防止并发操作覆盖会话。
// 保留最多 32 个旧摘要以支持并发重试，并以 KEEPTTL 保持绝对过期时间。
var adminRotateScript = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then return false end
local record = cjson.decode(value)
if record.digest ~= ARGV[1] then return false end
record.digest = ARGV[2]
record.previous = record.previous or {}
table.insert(record.previous, ARGV[1])
if #record.previous > 32 then table.remove(record.previous, 1) end
redis.call('SET', KEYS[1], cjson.encode(record), 'KEEPTTL')
return value
`)

// Rotate 校验当前或 previous 中的刷新令牌摘要，返回账号、SID 与新刷新令牌。
// 未匹配任何有效摘要时返回 operations.ErrInvalidRefresh。
func (s AdminSessions) Rotate(ctx context.Context, token string) (operations.AccountIdentity, string, string, error) {
	sid, oldSecret, ok := adminTokens.Parse(token)
	if !ok {
		return operations.AccountIdentity{}, "", "", operations.ErrInvalidRefresh
	}
	newSecret, err := auth.RandomSecret(32)
	if err != nil {
		return operations.AccountIdentity{}, "", "", err
	}
	result, err := adminRotateScript.Run(ctx, s.Redis, []string{adminTokens.Key(sid)}, sessiontoken.Digest(oldSecret), sessiontoken.Digest(newSecret)).Result()
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
	identity := operations.AccountIdentity{ID: record.AdminID, Username: record.Username, AuthVersion: record.AuthVersion}
	return identity, sid, adminTokens.TokenPrefix + sid + "." + newSecret, nil
}

// Revoke 校验令牌格式及当前或 previous 摘要后删除会话；验证失败返回 operations.ErrInvalidRefresh。
func (s AdminSessions) Revoke(ctx context.Context, token string) error {
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

// Exists 判断非空 sid 对应的会话是否存在，不加载账号信息。
func (s AdminSessions) Exists(ctx context.Context, sid string) (bool, error) {
	if sid == "" {
		return false, nil
	}
	n, err := s.Redis.Exists(ctx, adminTokens.Key(sid)).Result()
	return n == 1, err
}

// Matches 校验会话所属账号及权限版本；改密或改角色提升 AuthVersion，使旧会话失效。
func (s AdminSessions) Matches(ctx context.Context, sid string, id, version uint64) (bool, error) {
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
	return record.AdminID == id && record.AuthVersion == version, nil
}

// RevokeSession 直接按 sid 删除会话，不校验令牌摘要。
// 用于用户主动登出：此时调用方持有的是已通过鉴权的 sid，删除后该 sid 签发的令牌立即失效。
func (s AdminSessions) RevokeSession(ctx context.Context, sid string) error {
	if sid == "" {
		return operations.ErrInvalidRefresh
	}
	return s.Redis.Del(ctx, adminTokens.Key(sid)).Err()
}

// mfaChallengeTTL 限制密码验证通过后完成二次验证的时间窗口。
const mfaChallengeTTL = 5 * time.Minute

// BeginMFA 创建仅含账号身份的短期登录挑战；挑战不能代替 API 会话。
func (s AdminSessions) BeginMFA(ctx context.Context, account operations.AccountIdentity) (string, error) {
	token, err := auth.RandomSecret(32)
	if err != nil {
		return "", err
	}
	record, err := json.Marshal(adminSessionRecord{AuthVersion: account.AuthVersion, AdminID: account.ID, Username: account.Username})
	if err != nil {
		return "", err
	}
	if err := s.Redis.Set(ctx, mfaKey(token), record, mfaChallengeTTL).Err(); err != nil {
		return "", err
	}
	return token, nil
}

// ResolveMFA 仅在校验成功时原子消费登录挑战并返回账号 ID。
// 验证码错误时保留挑战以允许重试，不直接创建会话。
func (s AdminSessions) ResolveMFA(ctx context.Context, token string) (uint64, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, operations.ErrInvalidMFAChallenge
	}
	value, err := s.Redis.Get(ctx, mfaKey(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return 0, operations.ErrInvalidMFAChallenge
	}
	if err != nil {
		return 0, err
	}
	var record adminSessionRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return 0, operations.ErrInvalidMFAChallenge
	}
	return record.AdminID, nil
}

// mfaKey 把二次验证挑战码拼成 Redis 键，与会话键分开存放，
// 保证挑战记录不会被会话逻辑读成一条会话。
func mfaKey(token string) string { return "admin:mfa:" + token }

// adminTokens 保留后台专属前缀，通用编码与摘要由 platform 提供。
var adminTokens = sessiontoken.Layout{TokenPrefix: "ART_", KeyPrefix: "admin:session:"}
