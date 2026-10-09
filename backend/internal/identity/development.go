package identity

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// DevelopmentExchanger 仅由 LOGIN_MODE=development 装配，使用独立 openid 命名空间，无渠道调用。
type DevelopmentExchanger struct{}

var developmentAccount = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Exchange 校验开发 code 格式，返回 development: 身份或输入错误；无 I/O，context 无需外部传播。
func (DevelopmentExchanger) Exchange(_ context.Context, code string) (WeChatIdentity, error) {
	if !strings.HasPrefix(code, "dev:") || !developmentAccount.MatchString(strings.TrimPrefix(code, "dev:")) {
		return WeChatIdentity{}, fmt.Errorf("invalid development account")
	}
	return WeChatIdentity{OpenID: "development:" + strings.TrimPrefix(code, "dev:")}, nil
}
