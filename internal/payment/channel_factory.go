package payment

import (
	"context"
	"fmt"
)

// 支付渠道的构造。user 进程用它发起预付，central 进程用它执行退款派发——
// 两个进程都需要同一份渠道，因此构造逻辑只写在这里，模式校验也只在这里做一次。

// WechatCredentials 是微信支付直连所需的凭据。
type WechatCredentials struct {
	AppID             string
	MerchantID        string
	CertificateSerial string
	APIv3Key          string
	PrivateKeyPath    string
	NotifyURL         string
}

// PaymentChannel 是按模式构造出的支付渠道。Prepay 为 nil 表示该模式不支持预付
// （disabled），此时调用方不应挂载任何支付入口。
type PaymentChannel struct {
	Prepay PrepayProvider
	Refund RefundProvider
	Name   string // 模式名，随退款记录落库，用于回查时识别渠道
}

// NewPaymentChannel 按模式构造支付渠道。
// disabled 返回空渠道且不报错——没开支付是合法配置，不是错误。
func NewPaymentChannel(ctx context.Context, mode string, wechat WechatCredentials) (PaymentChannel, error) {
	switch mode {
	case "disabled":
		return PaymentChannel{Name: mode}, nil
	case "simulation":
		return PaymentChannel{Prepay: Simulator{}, Refund: Simulator{}, Name: mode}, nil
	case "wechat_direct":
		direct, err := NewWechatDirect(ctx, Config{
			AppID:             wechat.AppID,
			MerchantID:        wechat.MerchantID,
			CertificateSerial: wechat.CertificateSerial,
			APIv3Key:          wechat.APIv3Key,
			PrivateKeyPath:    wechat.PrivateKeyPath,
			NotifyURL:         wechat.NotifyURL,
		})
		if err != nil {
			return PaymentChannel{}, fmt.Errorf("wechat direct: %w", err)
		}
		return PaymentChannel{Prepay: direct, Refund: direct, Name: mode}, nil
	default:
		return PaymentChannel{}, fmt.Errorf("invalid payment mode %q", mode)
	}
}

// PrepayProvider 是发起支付预下单的渠道契约。定义在这里以便两个进程共用构造。
type PrepayProvider interface {
	Prepay(context.Context, PrepayRequest) (PrepayParams, error)
}
