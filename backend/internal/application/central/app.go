package central

import (
	"context"
	"errors"
	"log"
	"net/http"

	gatewayclient "wattharbor/internal/integration/gateway"
	"wattharbor/internal/payment"
	"wattharbor/internal/platform/config"
	"wattharbor/internal/platform/serviceclient"
)

// Prepare 装配本模块的处理器和资源，内部请求使用单体提供的客户端。
func Prepare(ctx context.Context, internalHTTP *http.Client) (_ http.Handler, _ func() error, err error) {
	cfg, err := config.LoadCentral()
	if err != nil {
		return nil, nil, err
	}
	resources, err := openResources(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	resources.InternalHTTP = internalHTTP
	app, err := assemble(ctx, cfg, resources)
	if err != nil {
		return nil, nil, errors.Join(err, resources.Close())
	}
	return app, resources.Close, nil
}

// assemble 创建退款渠道、网关客户端和内部路由，不启动监听。
func assemble(ctx context.Context, cfg config.Central, r *resources) (http.Handler, error) {
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
	router := newRouter(cfg, r, channel, gateway)
	log.Printf("central: payment=%s", cfg.PaymentMode)
	return router, nil
}
