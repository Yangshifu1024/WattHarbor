package user

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-pay/wechat-sdk/mini"
	"github.com/go-pay/xhttp"
	"wattharbor/internal/identity"
	gatewayclient "wattharbor/internal/integration/gateway"
	"wattharbor/internal/payment"
	"wattharbor/internal/platform/auth"
	"wattharbor/internal/platform/config"
	"wattharbor/internal/platform/serviceclient"
)

// Prepare 装配本模块的处理器和资源，内部请求使用单体提供的客户端。
func Prepare(ctx context.Context, internalHTTP *http.Client, notify func()) (_ http.Handler, _ func() error, err error) {
	cfg, err := config.LoadUser()
	if err != nil {
		return nil, nil, err
	}
	resources, err := openResources(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	resources.InternalHTTP = internalHTTP
	resources.Notify = notify
	app, err := assemble(ctx, cfg, resources)
	if err != nil {
		return nil, nil, errors.Join(err, resources.Close())
	}
	return app, resources.Close, nil
}

// assemble 创建会话签名、微信 SDK、支付渠道、网关客户端和路由。
// 微信请求超时为五秒；这里只组装依赖，资源由单体入口释放。
func assemble(ctx context.Context, cfg config.User, r *resources) (http.Handler, error) {
	jwt, err := auth.NewJWT(cfg.JWTSecret)
	if err != nil {
		return nil, err
	}
	wechat, err := mini.New(cfg.WeChatAppID, cfg.WeChatAppSecret, false)
	if err != nil {
		return nil, err
	}
	wechat.SetHttpClient(xhttp.NewClient().SetTimeout(5 * time.Second))
	channel, err := payment.NewPaymentChannel(ctx, cfg.PaymentMode, payment.WechatCredentials{
		AppID: cfg.WeChatAppID, MerchantID: cfg.WechatMchID,
		CertificateSerial: cfg.WechatCertSerial, APIv3Key: cfg.WechatAPIv3Key,
		PrivateKeyPath: cfg.WechatPrivateKey, NotifyURL: cfg.WechatNotifyURL,
	})
	if err != nil {
		return nil, err
	}

	gateway, err := gatewayclient.NewClient(serviceclient.Client{HTTP: r.InternalHTTP}, config.GatewayServiceURL, cfg.ServiceToken)
	if err != nil {
		return nil, err
	}
	router := newRouter(cfg, r, jwt, identity.MiniProgram{SDK: wechat}, channel, gateway)
	log.Printf("user: login=%s payment=%s", cfg.LoginMode, cfg.PaymentMode)
	return router, nil
}
