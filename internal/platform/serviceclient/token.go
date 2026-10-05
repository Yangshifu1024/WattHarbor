package serviceclient

import (
	"crypto/sha256"
	"crypto/subtle"
)

// VerifyServiceToken 校验服务间调用携带的令牌。
//
// 比较前先对两侧取 SHA-256 摘要，使不同长度的输入也能定长比较，
// 再用 ConstantTimeCompare 避免时序侧信道。期望令牌为空时一律拒绝——
// 未配置令牌意味着配置错误，放行等于让接口裸奔。
func VerifyServiceToken(expected, provided string) bool {
	if expected == "" {
		return false
	}
	want := sha256.Sum256([]byte(expected))
	got := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}
