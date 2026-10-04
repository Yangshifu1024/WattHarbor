package identity

import (
	"context"
	"errors"

	"github.com/go-pay/wechat-sdk/mini"
)

type WeChatIdentity struct{ OpenID, UnionID string }

type CodeExchanger interface {
	Exchange(context.Context, string) (WeChatIdentity, error)
}

type MiniProgram struct{ SDK *mini.SDK }

// ExchangePhone 返回微信验证的手机号；缺少授权结果时拒绝绑定。
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
