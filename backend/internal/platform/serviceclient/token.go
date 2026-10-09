package serviceclient

import (
	"crypto/sha256"
	"crypto/subtle"
)

// VerifyServiceToken 比较服务令牌；先取 SHA-256 摘要，再做固定时间比较，避免泄露长度和内容。
// 期望令牌为空时始终拒绝请求。
func VerifyServiceToken(expected, provided string) bool {
	if expected == "" {
		return false
	}
	want := sha256.Sum256([]byte(expected))
	got := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}
