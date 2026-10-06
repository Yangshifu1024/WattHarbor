package operations

import (
	"strings"
	"testing"
)

func TestValidTextRejectsBlankAndCountsRunes(t *testing.T) {
	if !ValidText("正常文本", 10) {
		t.Fatal("正常文本被拒")
	}
	if ValidText("", 10) || ValidText("   ", 10) || ValidText("\t\n", 10) {
		t.Fatal("空串与纯空白应判无效")
	}
	// 十个汉字是十个 rune，按字节算是三十——按 rune 计数才不会被中文提前超限
	if !ValidText(strings.Repeat("据", 10), 10) {
		t.Fatal("10 个汉字在上限 10 时应通过")
	}
	if ValidText(strings.Repeat("据", 11), 10) {
		t.Fatal("11 个汉字超出上限 10 应判无效")
	}
}

func TestValidPublicURLRejectsEverythingButPlainHTTPS(t *testing.T) {
	if !ValidPublicURL("https://example.com/hook") {
		t.Fatal("正常 HTTPS 链接被拒")
	}
	if !ValidPublicURL("https://example.com:8443/a?b=c#d") {
		t.Fatal("带端口与查询的 HTTPS 链接应通过")
	}
	bad := map[string]string{
		"明文 http":   "http://example.com/hook",
		"非 http 协议": "ftp://example.com",
		"无主机名":      "https:///path",
		"夹带凭据":      "https://user:pass@example.com/hook",
		"非字符串":      "not a url",
		"超长":        "https://example.com/" + strings.Repeat("a", 512),
	}
	for name, raw := range bad {
		if ValidPublicURL(raw) {
			t.Fatalf("%s 应判无效：%q", name, raw)
		}
	}
	// 刚好 512 字符是允许的上限
	if !ValidPublicURL("https://e.co/" + strings.Repeat("a", 512-13)) {
		t.Fatal("恰好 512 字符应通过")
	}
}

func TestValidRequestIDRequiresUUIDShape(t *testing.T) {
	if !ValidRequestID("11111111-2222-3333-4444-555555555555") {
		t.Fatal("合法 UUID 被拒")
	}
	if !ValidRequestID("11111111-2222-3333-4444-55555555555A") {
		t.Fatal("大写十六进制应通过")
	}
	for name, raw := range map[string]string{
		"空串":    "",
		"无连字符":  "11111111222233334444555555555555",
		"位数不足":  "1111-2222-3333",
		"非十六进制": "zzzzzzzz-2222-3333-4444-555555555555",
		"带前缀":   "req_11111111-2222-3333-4444-555555555555",
		"带空格":   " 11111111-2222-3333-4444-555555555555",
	} {
		if ValidRequestID(raw) {
			t.Fatalf("%s 应判无效", name)
		}
	}
}
