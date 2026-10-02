package redis

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/redis/go-redis/v9"
)

// 刷新令牌与 Redis 键的布局。用户端与后台各自有一份记录结构与轮换脚本，
// 但令牌的形状、散列、随机数生成和注销脚本完全一致，差异只在两个前缀上，
// 因此抽在这里共用，避免两处实现各自漂移。
//
// 令牌形如 <前缀><sid>.<secret>：sid 用来定位 Redis 键，secret 只以 SHA-256 散列入库，
// 明文仅在签发时返回一次。
type tokenLayout struct {
	tokenPrefix string // 令牌前缀，用于区分来源，如 RT_ / ART_
	keyPrefix   string // Redis 键前缀，如 user:session: / admin:session:
}

// 用户端刷新令牌布局。
var userTokens = tokenLayout{tokenPrefix: "RT_", keyPrefix: "user:session:"}

// 后台刷新令牌布局。
var adminTokens = tokenLayout{tokenPrefix: "ART_", keyPrefix: "admin:session:"}

// key 把会话 id 拼成 Redis 键，统一前缀便于按前缀排查和清理。
func (l tokenLayout) key(sid string) string { return l.keyPrefix + sid }

// mint 生成一对新的会话 id 与令牌密钥，并返回拼好的完整令牌。
// sid 取 16 字节、secret 取 32 字节随机数，编成不带填充的 base64url。
func (l tokenLayout) mint() (sid, token, secret string, err error) {
	sid, err = auth.RandomSecret(16)
	if err != nil {
		return "", "", "", err
	}
	secret, err = auth.RandomSecret(32)
	if err != nil {
		return "", "", "", err
	}
	return sid, l.tokenPrefix + sid + "." + secret, secret, nil
}

// parse 拆解刷新令牌，返回 sid 与密钥。
// 除了解析，还固定校验两段的长度（16 字节和 32 字节随机数编出的 base64url 长度）
// 并确认两段都能解回原字节——形状不对的一律判无效，不进 Redis 查询。
func (l tokenLayout) parse(token string) (string, string, bool) {
	if !strings.HasPrefix(token, l.tokenPrefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(token, l.tokenPrefix), ".")
	if len(parts) != 2 || len(parts[0]) != 22 || len(parts[1]) != 43 {
		return "", "", false
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[0]); err != nil {
		return "", "", false
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[1]); err != nil {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// digest 返回刷新令牌的 SHA-256 十六进制摘要，供 Redis 校验使用。
func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// revokeScript 是注销会话的 Lua 脚本：比对当前散列与 previous 里的旧散列，
// 命中就删掉整个会话键，不命中返回 0。校验和删除在 Redis 里一次完成。
var revokeScript = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then return 0 end
local record = cjson.decode(value)
if record.digest == ARGV[1] then redis.call('DEL', KEYS[1]); return 1 end
for _, digest in ipairs(record.previous or {}) do
  if digest == ARGV[1] then redis.call('DEL', KEYS[1]); return 1 end
end
return 0
`)
