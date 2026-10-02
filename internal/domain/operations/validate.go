package operations

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// 后台写入接口的通用输入校验。这些规则跨业务域复用——公告、券、报修、
// 提现、发票、webhook 都要判自由文本、链接和请求号——集中在领域层一份，
// 避免各文件各写一遍后又各自演化出不同口径。

// maxURLLength 是对外链接的整串长度上限。
const maxURLLength = 512

// uuidPattern 是客户端幂等请求号的形状。
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidText 校验自由文本字段：去掉首尾空白后不能为空，且按 rune 计数不超过 max。
// 按 rune 而不是字节，否则中文名会按字节提前超限。
func ValidText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max
}

// ValidPublicURL 只接受可对外访问的 HTTPS 链接。
//
// 条件缺一不可：能解析、scheme 为 https、有主机名、不夹带 user:pass 凭据、整串不超过上限。
// 拒绝 http 是因为明文可被中间人改写；拒绝 user:pass 是因为凭据会随每次回调发出去；
// 长度上限是为了让恶意超长串进不了存储与日志。
//
// Webhook 地址、白标 logo、协议链接与发票链接都走这条校验。
func ValidPublicURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && len(s) <= maxURLLength
}

// ValidRequestID 校验客户端幂等请求号必须是 UUID 形状。
// 非 UUID 会被拒绝而不是"生成一个"：请求号是幂等键，由客户端持有，
// 服务端替他造一个就等于放弃了幂等。
func ValidRequestID(v string) bool {
	return uuidPattern.MatchString(v)
}
