package payment

import (
	"context"
	"fmt"
)

// user 通过渠道发起支付，central 通过渠道派发退款；两者共用渠道构造与模式校验。

// WechatCredentials 是微信支付直连所需的凭据。
type WechatCredentials struct {
	AppID             string // 支付应用 ID
	MerchantID        string // 收款商户 ID
	CertificateSerial string // 商户签名证书序列号
	APIv3Key          string // 通知解密密钥，敏感
	PrivateKeyPath    string // 商户私钥文件路径，敏感
	NotifyURL         string // 渠道通知 URL
}

// PaymentChannel 是按模式构造出的支付渠道。Prepay 为 nil 表示该模式不支持预付
// （disabled），已注册适配器按依赖缺失返回不可用，不允许发起支付。
type PaymentChannel struct {
	Prepay PrepayProvider // 渠道预支付能力，disabled 为 nil
	Refund RefundProvider // 渠道退款查询及创建能力，disabled 为 nil
	Name   string         // disabled、simulation、wechat_direct 模式，供装配核对渠道
}

// NewPaymentChannel 按配置模式创建渠道；disabled 返回空渠道，不报错。
// 真实模式校验微信凭据，未知模式或 SDK 初始化失败返回错误。
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
	// Prepay 在 context 下为申请准备参数，返回参数或可能受理未知的错误；不写业务到账，调用方按同一单号恢复。
	Prepay(context.Context, PrepayRequest) (PrepayParams, error)
}
