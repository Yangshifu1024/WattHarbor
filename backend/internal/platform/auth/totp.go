package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// 按 RFC 6238 在本地生成和验证动态口令，无需外部服务。

const (
	totpPeriod    = 30 * time.Second // 动态口令的时间步长，标准取 30 秒。
	totpDigits    = 6                // 口令位数，与主流验证器 App 的默认一致。
	totpSkew      = 1                // 允许前后各一个时间步，容忍服务器和手机的时钟偏差。
	totpSecretLen = 20               // 共享密钥字节数，取 RFC 4226 建议的 160 位。
)

var (
	// ErrInvalidTOTP 表示给的口令没有命中任何一个可接受的时间步。
	ErrInvalidTOTP = errors.New("验证码无效或已过期")
	// base32Encoding 是密钥的存储编码：无填充大写，验证时容忍用户手工输入的空格。
	base32Encoding = base32.StdEncoding.WithPadding(base32.NoPadding)
)

// NewTOTPSecret 生成 20 字节随机密钥，编码为验证器使用的 base32。
func NewTOTPSecret() (string, error) {
	raw := make([]byte, totpSecretLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base32Encoding.EncodeToString(raw), nil
}

// TOTPURI 生成验证器绑定链接；issuer、account 和 secret 不能为空。
// 使用 SHA1、六位口令和三十秒周期。
func TOTPURI(issuer, account, secret string) (string, error) {
	if issuer == "" || account == "" || secret == "" {
		return "", errors.New("issuer, account and secret are required")
	}
	query := url.Values{}
	query.Set("secret", secret)
	query.Set("issuer", issuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", fmt.Sprint(totpDigits))
	query.Set("period", fmt.Sprint(int(totpPeriod/time.Second)))
	return (&url.URL{
		Scheme:   "otpauth",
		Host:     "totp",
		Path:     "/" + issuer + ":" + account,
		RawQuery: query.Encode(),
	}).String(), nil
}

// VerifyTOTP 校验六位动态口令，允许当前时间步前后各一步。
// 返回命中的时间步及错误；多个时间步碰撞时选最大一步。失败的时间步不可使用。
// 检查所有允许时间步，并用固定时间比较减少时序信息泄露；本函数不消费，调用方负责原子持久化。
func VerifyTOTP(secret, code string, now time.Time) (int64, error) {
	secret = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, ErrInvalidTOTP
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return 0, ErrInvalidTOTP
		}
	}
	key, err := base32Encoding.DecodeString(secret)
	if err != nil || len(key) == 0 {
		return 0, ErrInvalidTOTP
	}
	step := now.Unix() / int64(totpPeriod/time.Second)
	var matched bool
	var matchedStep int64
	for offset := -totpSkew; offset <= totpSkew; offset++ {
		expected, err := hotp(key, uint64(step+int64(offset)))
		if err != nil {
			return 0, err
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			matched = true
			matchedStep = step + int64(offset)
		}
	}
	if !matched {
		return 0, ErrInvalidTOTP
	}
	return matchedStep, nil
}

// hotp 按 RFC 4226 用 HMAC-SHA1 和动态截断生成六位口令。
func hotp(key []byte, counter uint64) (string, error) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	if _, err := mac.Write(buf[:]); err != nil {
		return "", err
	}
	sum := mac.Sum(nil)
	// 按 RFC 4226 第 5.3 节做动态截断。
	offset := sum[len(sum)-1] & 0x0f
	value := int64(sum[offset]&0x7f)<<24 | int64(sum[offset+1])<<16 | int64(sum[offset+2])<<8 | int64(sum[offset+3])
	mod := int32(value % 1_000_000)
	return fmt.Sprintf("%0*d", totpDigits, mod), nil
}

// TOTPCode 返回当前时间步的验证码，供 MFA 登记验证使用；不应用时间步容差。
func TOTPCode(secret string, now time.Time) (string, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	key, err := base32Encoding.DecodeString(normalized)
	if err != nil {
		return "", err
	}
	return hotp(key, uint64(now.Unix()/int64(totpPeriod/time.Second)))
}

// RandomSecret 生成 n 字节随机数并编码为无填充 base64url，用于会话、刷新令牌和 MFA 挑战。
func RandomSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
