package money

// MaxPANLength 是卡号的最大位数，取 ISO/IEC 7812 的 PAN 上限 19 位。
// 库里的列是 varchar(64)，那是存储余量，不是"卡号可以到 64 位"。
const MaxPANLength = 19

// CanonicalCardNumber 判断卡号是否为规范形式：十进制、无前导零、长度在 1–19 位。
// 规范形式保证卡号与协议的 BCD 编码一一对应，不存在两个卡号映射到同一张卡。
//
// 这里逐字符判定而不是转成整数：早先的实现是 ParseUint(card, 10, 32)，
// 位宽 32 只能容下约十位，于是任何真实卡号（16–19 位）都会被判为非法，
// 后台绑定在线卡的接口对真卡号完全不可用。卡号是要原样落库并写进 BCD
// 报文的值，不需要能转成整数。
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
