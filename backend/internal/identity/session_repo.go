package identity

import (
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/sessiontoken"
	"context"
	"encoding/json"
	"errors"
	"github.com/redis/go-redis/v9"
	"strconv"
	"time"
)

// ErrInvalidRefresh 表示刷新令牌格式错误、摘要不匹配或会话已过期。
var ErrInvalidRefresh = errors.New("invalid or expired refresh token")

// RefreshTTL 是刷新令牌的 Redis 存活时长。会话记录以该 TTL 写入。
// 外部改写记录时必须沿用同一时长，因此对外可见。
const RefreshTTL = 7 * 24 * time.Hour

// sessionRecord 保存用户标识与摘要，不持久保存刷新明文。
type sessionRecord struct {
	UserID   sessionUserID `json:"uid"`                // 用户精确身份
	OpenID   string        `json:"openid"`             // 登录 openid，用户类型令牌签发所需
	Digest   string        `json:"digest"`             // 当前刷新密钥 SHA-256 摘要
	Previous []string      `json:"previous,omitempty"` // 最多32个旧摘要，仅支持令牌注销
}

// sessionUserID 用精确十进制字符串避开 Lua cjson 浮点舍入，同时兼容旧整数记录。
type sessionUserID uint64

// MarshalJSON 将 id 写成精确十进制字符串，返回 JSON 或序列化错误。
func (id sessionUserID) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatUint(uint64(id), 10))
}

// UnmarshalJSON 从 raw 精确读取字符串或旧整数，成功赋值 id，非法、零、溢出返回错误。
func (id *sessionUserID) UnmarshalJSON(raw []byte) error {
	value := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
	}
	if value == "" {
		return ErrInvalidRefresh
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return ErrInvalidRefresh
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return ErrInvalidRefresh
	}
	*id = sessionUserID(parsed)
	return nil
}

// SessionRepo 仅负责用户会话记录；Redis 生命周期由进程管理。
type SessionRepo struct {
	Redis *redis.Client /* 进程负责的用户会话连接 */
}

// Create 保存绝对有效期为七天的会话，返回 SID 与刷新令牌；轮换不能延长该期限。
func (s SessionRepo) Create(ctx context.Context, user User) (string, string, error) {
	sid, token, secret, err := userTokens.Mint()
	if err != nil {
		return "", "", err
	}
	record, err := json.Marshal(sessionRecord{UserID: sessionUserID(user.ID), OpenID: user.OpenID, Digest: sessiontoken.Digest(secret)})
	if err != nil {
		return "", "", err
	}
	if err := s.Redis.Set(ctx, userTokens.Key(sid), record, RefreshTTL).Err(); err != nil {
		return "", "", err
	}
	return sid, token, nil
}

// Rotate 校验 token 当前摘要并轮换，返回用户、SID、新刷新令牌或格式、Redis 错误。
// 同一旧令牌只允许一个轮换获胜；previous 仅用于注销，不支持重复刷新，不延长会话寿命。
func (s SessionRepo) Rotate(ctx context.Context, token string) (User, string, string, error) {
	sid, oldSecret, ok := userTokens.Parse(token)
	if !ok {
		return User{}, "", "", ErrInvalidRefresh
	}
	newSecret, err := auth.RandomSecret(32)
	if err != nil {
		return User{}, "", "", err
	}
	result, err := sessiontoken.RotateScript.Run(ctx, s.Redis, []string{userTokens.Key(sid)}, sessiontoken.Digest(oldSecret), sessiontoken.Digest(newSecret), "uid").Result()
	if errors.Is(err, redis.Nil) {
		return User{}, "", "", ErrInvalidRefresh
	}
	if err != nil {
		return User{}, "", "", err
	}
	if result == nil || result == int64(0) {
		return User{}, "", "", ErrInvalidRefresh
	}
	value, ok := result.(string)
	if !ok {
		return User{}, "", "", ErrInvalidRefresh
	}
	var record sessionRecord
	if err := json.Unmarshal([]byte(value), &record); err != nil {
		return User{}, "", "", err
	}
	return User{ID: uint64(record.UserID), OpenID: record.OpenID}, sid, "RT_" + sid + "." + newSecret, nil
}

// Revoke 用当前或 previous 摘要原子注销会话，不接受其他令牌。
func (s SessionRepo) Revoke(ctx context.Context, token string) error {
	sid, secret, ok := userTokens.Parse(token)
	if !ok {
		return ErrInvalidRefresh
	}
	result, err := sessiontoken.RevokeScript.Run(ctx, s.Redis, []string{userTokens.Key(sid)}, sessiontoken.Digest(secret)).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return ErrInvalidRefresh
	}
	return nil
}

// Exists 核对 sid 会话存在性，返回检查结果或 Redis 错误，空 SID 为 false；只读。
func (s SessionRepo) Exists(ctx context.Context, sid string) (bool, error) {
	if sid == "" {
		return false, nil
	}
	n, err := s.Redis.Exists(ctx, userTokens.Key(sid)).Result()
	return n == 1, err
}

// userTokens 保留 C 端刷新令牌和会话键命名，通用编码由 platform 提供。
var userTokens = sessiontoken.Layout{TokenPrefix: "RT_", KeyPrefix: "user:session:"}
