package serviceclient

import (
	"crypto/sha256"
	"crypto/subtle"
)

// VerifyServiceToken 将双方令牌转为等长的 SHA-256 摘要，再用固定时间比较。
// 期望令牌为空时始终拒绝请求。
func VerifyServiceToken(expected, provided string) bool {
	if expected == "" {
		return false
	}
	want := sha256.Sum256([]byte(expected))
	got := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}
