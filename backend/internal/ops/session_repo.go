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

// SessionRepo 管理后台会话，底层就是 Redis 里的 admin:session:<sid> 键。
// 结构体只持有 Redis 客户端（字段 Redis），没有本地状态，可以直接按值复制。
type SessionRepo struct {
	Redis *redis.Client /* 进程拥有的后台会话连接 */
}

// Create 在ctx下为account创建会话，返回SID、完整刷新令牌或随机／存储错误。
// 令牌形如 ART_<sid>.<secret>，完整明文只在这里返回一次，之后客户端凭它换新令牌。
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

// Rotate 在ctx下校验token当前摘要，返回账号、SID、新刷新令牌或凭据／存储错误。
// previous仅可注销；数字ID／版本从旧JSON原文本恢复，连续轮换保持精度和绝对TTL。
// 未匹配任何有效摘要时返回 operations.ErrInvalidRefresh。
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

// Revoke 在ctx下校验token格式及当前／previous摘要后原子删除会话，返回凭据／存储错误。
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

// Exists 在ctx下核对sid会话存在性，返回事实或Redis错误；空SID为false，只读。
func (s SessionRepo) Exists(ctx context.Context, sid string) (bool, error) {
	if sid == "" {
		return false, nil
	}
	n, err := s.Redis.Exists(ctx, adminTokens.Key(sid)).Result()
	return n == 1, err
}

// Matches 在ctx下核对sid原记录的id／version，返回匹配事实或存储／记录错误，只读。
// 改密或改角色提升AuthVersion使旧会话失效；新字符串及旧数字记录均精确解码。
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

// RevokeSession 在ctx下直接删除sid会话，返回空身份／Redis错误，不校验令牌摘要。
// 用于用户主动登出：此时调用方持有的是已通过鉴权的 sid，删除后该 sid 签发的令牌立即失效。
func (s SessionRepo) RevokeSession(ctx context.Context, sid string) error {
	if sid == "" {
		return operations.ErrInvalidRefresh
	}
	return s.Redis.Del(ctx, adminTokens.Key(sid)).Err()
}

// mfaChallengeTTL 限制密码验证通过后完成二次验证的时间窗口。
const mfaChallengeTTL = 5 * time.Minute

// BeginMFA 在ctx下为account保存5分钟挑战，返回随机挑战或随机／编码／存储错误。
// 挑战只保存原身份和版本，不代替API会话，也不保存密码或验证码。
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

// MFAChallenge 保存所属存储读出的原身份与私有CAS凭据，不作为HTTP公开响应。
type MFAChallenge struct {
	Account operations.AccountIdentity // 原密码验证时的账号及权限版本
	token   string                     // 原随机挑战，决定唯一存储键
	raw     string                     // 原JSON文本，用于拒绝并发替换／重复消费
}

// ResolveMFA 在ctx下读取token原挑战身份与CAS凭据，返回格式／缺失／存储错误，不消费记录。
// 验证码错误时保留挑战以允许重试，不直接创建会话。
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

// ConsumeMFA 在ctx下比较challenge原JSON并原子删除，仅原读取获胜一次；缺失／替换返回失效挑战。
// Redis执行错误原样返回，不能降级为可重放的成功登录。
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

// mfaKey 把二次验证挑战码拼成 Redis 键，与会话键分开存放，
// 保证挑战记录不会被会话逻辑读成一条会话。
func mfaKey(token string) string { return "admin:mfa:" + token }

// adminTokens 保留后台专属前缀，通用编码与摘要由 platform 提供。
var adminTokens = sessiontoken.Layout{TokenPrefix: "ART_", KeyPrefix: "admin:session:"}
