package payment

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/downloader"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/jsapi"
	"github.com/wechatpay-apiv3/wechatpay-go/services/refunddomestic"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

// ErrInvalidPayment 表示渠道配置、请求或已解析回执缺少可信身份及金额，不能用于资金确认。
var ErrInvalidPayment = errors.New("invalid or unverified payment")

// wechatOrderNumber 按渠道要求限制为 6–32 个允许的 ASCII 字符，商户范围内保持唯一。
var wechatOrderNumber = regexp.MustCompile(`^[0-9A-Za-z_\-|*]{6,32}$`)

// PrepayRequest 是已经持久化的原支付申请，向渠道请求参数不等于到账。
type PrepayRequest struct {
	MerchantOrderNo string    // 原商户支付号，渠道重试沿用
	OpenID          string    // 已核对用户的微信身份，不公开到支付回执
	AmountCents     int64     // 原实际应付金额，分，必须为正
	ExpiresAt       time.Time // 原支付有效期，真实预支付要求仍未过期
}

// PrepayParams 保持客户端调用微信支付所需的原参数形状；模拟渠道只填写原模拟字段。
type PrepayParams struct {
	Provider  string `json:"provider"`  // wechat_direct 或 simulation，后续确认及退款据此识别渠道
	PrepayID  string `json:"prepay_id"` // 渠道预支付身份，同支付首次保存后复用
	AppID     string `json:"appId"`     // 原支付应用 ID
	TimeStamp string `json:"timeStamp"` // 渠道签名使用的秒时间戳文本
	NonceStr  string `json:"nonceStr"`  // 渠道签名使用的随机串
	Package   string `json:"package"`   // 原 prepay_id 参数包
	SignType  string `json:"signType"`  // 客户端签名算法标签
	PaySign   string `json:"paySign"`   // 客户端支付签名，可公开给原付款人，不是商户私钥
}

// VerifiedTransaction 只由渠道验签解密后的成功 JSAPI 通知构造，资金用例还须核对原支付关联。
type VerifiedTransaction struct {
	Provider        string    // 已验签渠道标签
	MerchantID      string    // 已验签收款商户
	AppID           string    // 已验签支付应用
	MerchantOrderNo string    // 原商户支付号
	TransactionID   string    // 渠道交易唯一身份
	OpenID          string    // 已验签付款人身份
	PaidCents       int64     // 渠道成功支付额，CNY 分
	PaidAt          time.Time // 渠道成功时刻，规范化为 UTC
}

// Config 是微信直连构造配置，敏感值只用于 SDK，不进入公开回执或业务用例。
type Config struct {
	AppID             string // 支付应用 ID
	MerchantID        string // 收款商户 ID
	CertificateSerial string // 商户签名证书序列号
	APIv3Key          string // 32 字节通知解密密钥，敏感
	PrivateKeyPath    string // 商户私钥文件路径，敏感资源由 SDK 读取
	NotifyURL         string // 渠道支付通知回调 URL
}

// WechatDirect 封装官方 SDK 的预支付、验签解密和退款能力，不持有业务数据库。
type WechatDirect struct {
	config  Config                           // 固定商户与应用配置，不随通知内容替换
	refunds refunddomestic.RefundsApiService // 官方 SDK 退款查询及创建客户端
	jsapi   jsapi.JsapiApiService            // 官方 SDK JSAPI 预支付客户端
	notify  *notify.Handler                  // 通知签名验证及 APIv3 解密处理器
}

// NewWechatDirect 在 ctx 下按 config 校验凭据、读取私钥并构造官方 SDK 及证书访问器。
// 返回可复用渠道；配置不完整为 ErrInvalidPayment，密钥文件和 SDK 初始化错误原样返回，不写业务状态。
func NewWechatDirect(ctx context.Context, config Config) (*WechatDirect, error) {
	if config.AppID == "" || config.MerchantID == "" || config.CertificateSerial == "" || len(config.APIv3Key) != 32 ||
		config.PrivateKeyPath == "" || config.NotifyURL == "" {
		return nil, ErrInvalidPayment
	}
	privateKey, err := utils.LoadPrivateKeyWithPath(config.PrivateKeyPath)
	if err != nil {
		return nil, err
	}
	client, err := core.NewClient(ctx, option.WithWechatPayAutoAuthCipher(config.MerchantID, config.CertificateSerial, privateKey, config.APIv3Key))
	if err != nil {
		return nil, err
	}
	visitor := downloader.MgrInstance().GetCertificateVisitor(config.MerchantID)
	return &WechatDirect{config: config, refunds: refunddomestic.RefundsApiService{Client: client}, jsapi: jsapi.JsapiApiService{Client: client},
		notify: notify.NewNotifyHandler(config.APIv3Key, verifiers.NewSHA256WithRSAVerifier(visitor))}, nil
}

// Prepay 在 ctx 下为 request 原单向渠道准备支付参数，返回完整参数或输入／SDK／缺字段错误。
// 错误可能发生在渠道受理之后，调用方须保留原单号恢复，不根据本次响应推进到账。
func (w *WechatDirect) Prepay(ctx context.Context, request PrepayRequest) (PrepayParams, error) {
	if w == nil || !wechatOrderNumber.MatchString(request.MerchantOrderNo) || request.OpenID == "" || request.AmountCents <= 0 || !request.ExpiresAt.After(time.Now()) {
		return PrepayParams{}, ErrInvalidPayment
	}
	resp, _, err := w.jsapi.PrepayWithRequestPayment(ctx, jsapi.PrepayRequest{
		Appid: new(w.config.AppID), Mchid: new(w.config.MerchantID),
		Description: new("电瓶车充电"), OutTradeNo: new(request.MerchantOrderNo),
		TimeExpire: new(request.ExpiresAt), NotifyUrl: new(w.config.NotifyURL),
		Amount: &jsapi.Amount{Total: new(request.AmountCents), Currency: new("CNY")},
		Payer:  &jsapi.Payer{Openid: new(request.OpenID)},
	})
	if err != nil {
		return PrepayParams{}, err
	}
	if resp == nil || resp.PrepayId == nil || resp.Appid == nil || resp.TimeStamp == nil || resp.NonceStr == nil || resp.Package == nil || resp.SignType == nil || resp.PaySign == nil {
		return PrepayParams{}, ErrInvalidPayment
	}
	return PrepayParams{Provider: "wechat_direct", PrepayID: *resp.PrepayId, AppID: *resp.Appid,
		TimeStamp: *resp.TimeStamp, NonceStr: *resp.NonceStr, Package: *resp.Package,
		SignType: *resp.SignType, PaySign: *resp.PaySign}, nil
}

// VerifyNotification 在 ctx 下用官方 SDK 验证 request 签名并解密，仅读取成功 CNY／JSAPI 通知。
// 返回已验签交易；签名、解密、状态、字段、商户／应用或时间不符返回错误，无资金或数据库写入。
func (w *WechatDirect) VerifyNotification(ctx context.Context, request *http.Request) (VerifiedTransaction, error) {
	if w == nil || request == nil {
		return VerifiedTransaction{}, ErrInvalidPayment
	}
	transaction := new(payments.Transaction)
	if _, err := w.notify.ParseNotifyRequest(ctx, request, transaction); err != nil {
		return VerifiedTransaction{}, err
	}
	if transaction.TradeState == nil || *transaction.TradeState != "SUCCESS" || transaction.Mchid == nil || transaction.Appid == nil ||
		transaction.OutTradeNo == nil || transaction.TransactionId == nil || transaction.SuccessTime == nil ||
		transaction.Amount == nil || transaction.Amount.Total == nil || transaction.Amount.Currency == nil || *transaction.Amount.Currency != "CNY" ||
		transaction.Payer == nil || transaction.Payer.Openid == nil || transaction.TradeType == nil || *transaction.TradeType != "JSAPI" {
		return VerifiedTransaction{}, ErrInvalidPayment
	}
	paidAt, err := time.Parse(time.RFC3339, *transaction.SuccessTime)
	if err != nil || *transaction.Mchid != w.config.MerchantID || *transaction.Appid != w.config.AppID {
		return VerifiedTransaction{}, ErrInvalidPayment
	}
	return VerifiedTransaction{Provider: "wechat_direct", MerchantID: *transaction.Mchid, AppID: *transaction.Appid,
		MerchantOrderNo: *transaction.OutTradeNo, TransactionID: *transaction.TransactionId,
		OpenID: *transaction.Payer.Openid, PaidCents: *transaction.Amount.Total, PaidAt: paidAt.UTC()}, nil
}
