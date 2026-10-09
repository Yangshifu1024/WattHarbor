package money

// MaxPANLength 限制卡号最多 19 位。
// 数据库列长度为 64，不改变业务校验上限。
const MaxPANLength = 19

// CanonicalCardNumber 校验 1–19 位十进制卡号，拒绝前导零。
// 逐字符校验，不转整数，避免长卡号溢出。
func CanonicalCardNumber(card string) bool {
	if card == "" || len(card) > MaxPANLength {
		return false
	}
	// 前导零会让 "007" 与 "7" 指向同一张卡，规范形式直接拒绝。
	if card[0] == '0' {
		return false
	}
	for i := 0; i < len(card); i++ {
		if card[i] < '0' || card[i] > '9' {
			return false
		}
	}
	return true
}
