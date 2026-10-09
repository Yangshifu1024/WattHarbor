package operations

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// 后台写入接口共用文本、链接与幂等请求号的校验规则。

// maxURLLength 是对外链接的整串长度上限。
const maxURLLength = 512

// uuidPattern 匹配客户端 UUID 请求号。
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidText 校验自由文本字段：去掉首尾空白后不能为空，且按 rune 计数不超过 max。
// 按 rune 而不是字节，否则中文名会按字节提前超限。
func ValidText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max
}

// ValidPublicURL 只接受长度合规、有主机名且不含 user:pass 凭据的 HTTPS 链接。
func ValidPublicURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && len(s) <= maxURLLength
}

// ValidRequestID 只接受 UUID 请求号；请求号由客户端持有，不能由服务端另造以绕过幂等。
func ValidRequestID(v string) bool {
	return uuidPattern.MatchString(v)
}
