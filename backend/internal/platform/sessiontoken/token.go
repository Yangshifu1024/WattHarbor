package sessiontoken

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"chargepilot/internal/platform/auth"
	"github.com/redis/go-redis/v9"
)

// Layout 配置令牌和 Redis 键前缀。
// 令牌格式为 <TokenPrefix><sid>.<secret>，存储只保存 secret 的 SHA-256 摘要。
type Layout struct {
	TokenPrefix string // 调用方定义的令牌来源前缀
	KeyPrefix   string // 调用方定义的存储键前缀
}

// Key 用配置前缀和会话 ID 生成存储键。
func (l Layout) Key(sid string) string { return l.KeyPrefix + sid }

// Mint 生成会话 ID、完整令牌和随机密钥，不写入存储。
// 会话 ID 使用 16 字节随机数，密钥使用 32 字节，均编码为无填充的 base64url。
func (l Layout) Mint() (sid, token, secret string, err error) {
	sid, err = auth.RandomSecret(16)
	if err != nil {
		return "", "", "", err
	}
	secret, err = auth.RandomSecret(32)
	if err != nil {
		return "", "", "", err
	}
	return sid, l.TokenPrefix + sid + "." + secret, secret, nil
}

// Parse 检查令牌前缀、两段长度和 base64url 编码，返回会话 ID、密钥及是否有效。
// 两段分别必须能还原为 16 和 32 字节；格式错误无需查询 Redis。
func (l Layout) Parse(token string) (string, string, bool) {
	if !strings.HasPrefix(token, l.TokenPrefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(token, l.TokenPrefix), ".")
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

// Digest 返回密钥的 SHA-256 十六进制摘要。
func Digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// RotateScript 在 Redis 中核对旧摘要并更新为新摘要，保留 32 个旧摘要和键的 TTL。
// KEYS[1] 是记录键，ARGV[1] 是旧摘要，ARGV[2] 是新摘要；其余参数指定需保留精度的无符号整数字段。
// 返回轮换前记录；无记录、摘要不符或整数无法恢复时返回 false，Redis 或 JSON 错误直接返回。
var RotateScript = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then return false end
local record = cjson.decode(value)
if record.digest ~= ARGV[1] then return false end
for i = 3, #ARGV do
  local field = ARGV[i]
  if type(record[field]) == 'number' then
    local original = string.match(value, '"' .. field .. '"%s*:%s*(%d+)%s*[,}]')
    if not original then return false end
    record[field] = original
  end
end
record.digest = ARGV[2]
record.previous = record.previous or {}
table.insert(record.previous, ARGV[1])
if #record.previous > 32 then table.remove(record.previous, 1) end
redis.call('SET', KEYS[1], cjson.encode(record), 'KEEPTTL')
return value
`)

// RevokeScript 在 Redis 中比对当前摘要和历史摘要，命中时删除整个会话键。
// KEYS[1] 是会话键，ARGV[1] 是待校验摘要；未命中返回 0，检查和删除一次完成。
var RevokeScript = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then return 0 end
local record = cjson.decode(value)
if record.digest == ARGV[1] then redis.call('DEL', KEYS[1]); return 1 end
for _, digest in ipairs(record.previous or {}) do
  if digest == ARGV[1] then redis.call('DEL', KEYS[1]); return 1 end
end
return 0
`)
