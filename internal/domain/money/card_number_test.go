package money

import "testing"

// 真实卡号是 16–19 位。早先按 uint32 解析，任何真实卡号都会被判非法，
// 这组用例把该回归钉住：12 位以上必须放行。
func TestCanonicalCardNumberAcceptsRealPAN(t *testing.T) {
	for _, card := range []string{
		"6225880212345678", // 16 位，真实格式
		"6222021234567890",
		"4111111111111111", // 16 位
		"375556917985515",  // 15 位
		"1234567890123456789",
		"1", // 位数不设下限，只要形态规范
	} {
		if !CanonicalCardNumber(card) {
			t.Fatalf("%q 应判为规范卡号", card)
		}
	}
}

func TestCanonicalCardNumberRejectsNonCanonical(t *testing.T) {
	for name, card := range map[string]string{
		"空串":      "",
		"前导零":     "0123456789012345",
		"只有一个零":   "0",
		"含字母":     "62258802123456AB",
		"含连字符":    "6225-8802-1234-5678",
		"含空格":     " 6225880212345678",
		"负号":      "-6225880212345678",
		"超过 19 位": "12345678901234567890",
		"十六进制写法":  "0x1234",
	} {
		if CanonicalCardNumber(card) {
			t.Fatalf("%s: %q 不应判为规范卡号", name, card)
		}
	}
}

// 规范形式的目的是"同一个号码只有一种写法"，前导零与超长都要挡住。
func TestCanonicalCardNumberIsOneRepresentationPerNumber(t *testing.T) {
	if CanonicalCardNumber("007") == CanonicalCardNumber("7") {
		t.Fatal("带前导零与不带前导零不应同时合法")
	}
}
