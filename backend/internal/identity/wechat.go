package identity

import (
	"context"
	"errors"

	"github.com/go-pay/wechat-sdk/mini"
)

// WeChatIdentity 保留服务端代码交换得到的登录身份。
type WeChatIdentity struct {
	OpenID, UnionID string /* 小程序身份及可选开放平台身份 */
}

// CodeExchanger 定义登录用例所需的代码换身份能力，调用方管理请求取消；实现只返回渠道身份，不创建用户或会话。
type CodeExchanger interface {
	// Exchange 在请求 context 下交换一次代码，返回微信身份或渠道、响应错误，不写用户。
	Exchange(context.Context, string) (WeChatIdentity, error)
}

// MiniProgram 适配 cmd 负责的小程序 SDK；凭据不传入其他业务模块。
type MiniProgram struct {
	SDK *mini.SDK /* 进程装配的小程序渠道能力 */
}

// ExchangePhone 交换 code，返回服务端验证的手机号或渠道、缺失响应错误，不写用户。
func (m MiniProgram) ExchangePhone(ctx context.Context, code string) (string, error) {
	result, err := m.SDK.GetPhoneNumber(ctx, code)
	if err != nil {
		return "", err
	}
	if result == nil || result.Errcode != 0 || result.PhoneInfo == nil {
		return "", errors.New("invalid phone authorization")
	}
	return result.PhoneInfo.PurePhoneNumber, nil
}

// Exchange 交换 code，返回 openID/unionID 或渠道、缺失身份错误，不创建会话。
func (m MiniProgram) Exchange(ctx context.Context, code string) (WeChatIdentity, error) {
	session, err := m.SDK.Code2Session(ctx, code)
	if err != nil {
		return WeChatIdentity{}, err
	}
	if session.Openid == "" {
		return WeChatIdentity{}, errors.New("WeChat returned no openid")
	}
	return WeChatIdentity{OpenID: session.Openid, UnionID: session.Unionid}, nil
}
