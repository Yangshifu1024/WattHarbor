package sessiontoken

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/redis/go-redis/v9"
)

// Layout 配置令牌与存储键前缀；随机数、编码、摘要和通用撤销机制不包含业务来源。
// 明文格式为 <TokenPrefix><sid>.<secret>，只将 secret 的 SHA-256 摘要存入记录。
type Layout struct {
	TokenPrefix string // 调用方定义的令牌来源前缀
	KeyPrefix   string // 调用方定义的存储键前缀
}

// Key 将 sid 原会话身份拼成配置的存储键，返回文本，无 IO。
func (l Layout) Key(sid string) string { return l.KeyPrefix + sid }

// Mint 无参数，生成 SID、完整明文及原随机密钥；返回随机源错误，不写入会话。
// sid 取 16 字节、secret 取 32 字节随机数，编成不带填充的 base64url。
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

// Parse 核对 token 的配置前缀与编码，返回 SID、原密钥及是否有效，无 IO。
// 除了解析，还固定校验两段的长度（16 字节和 32 字节随机数编出的 base64url 长度）
// 并确认两段都能解回原字节——形状不对的一律判无效，不进 Redis 查询。
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

// Digest 将 secret 原密钥转为 SHA-256 十六进制文本，无随机数或 IO。
func Digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// RevokeScript 以 KEYS[1] 和 ARGV[1] 原摘要原子注销会话：比对当前散列与 previous 里的旧散列，
// 命中就删掉整个会话键，不命中返回 0。校验和删除在 Redis 里一次完成。
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
