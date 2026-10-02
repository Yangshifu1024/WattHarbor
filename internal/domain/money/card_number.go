package money

import "strconv"

// CanonicalCardNumber 判断卡号是否为规范形式：十进制、无前导零、可用 uint32 表示。
// 规范形式保证卡号与协议的 BCD 编码一一对应，不存在两个卡号映射到同一张卡。
func CanonicalCardNumber(card string) bool {
	n, err := strconv.ParseUint(card, 10, 32)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == card
}
