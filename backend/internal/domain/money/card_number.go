package money

// MaxPANLength 是卡号的最大位数，取 ISO/IEC 7812 的 PAN 上限 19 位。
// 库里的列是 varchar(64)，那是存储余量，不是"卡号可以到 64 位"。
const MaxPANLength = 19

// CanonicalCardNumber 校验 1–19 位十进制卡号，拒绝前导零。
// 逐字符校验，不转整数，避免截断长卡号；规范形式与协议 BCD 编码一一对应。
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
