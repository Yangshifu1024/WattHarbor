package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/config"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
)

// run 加载配置并启动应用，等待请求和任务退出后释放资源；返回启动、运行和清理错误。
func run(ctx context.Context) (err error) {
	cfg, err := config.LoadCentral()
	if err != nil {
		return err
	}
	resources, err := openResources(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resources.Close()) }()
	app, err := assemble(ctx, cfg, resources)
	if err != nil {
		return err
	}
	return app.Run(ctx)
}

// application 持有已组装的 HTTP 服务，资源由 run 释放。
type application struct {
	// Server 已装配的 HTTP 服务器，由 httpapi.Serve 独占管理监听、请求 context 与退出等待。
	Server *http.Server
}

// assemble 创建退款渠道、网关客户端和内部路由，不启动监听。
func assemble(ctx context.Context, cfg config.Central, r *resources) (*application, error) {
	channel, err := payment.NewPaymentChannel(ctx, cfg.PaymentMode, payment.WechatCredentials{
		AppID: cfg.WeChatAppID, MerchantID: cfg.WechatMchID,
		CertificateSerial: cfg.WechatCertSerial, APIv3Key: cfg.WechatAPIv3Key,
		PrivateKeyPath: cfg.WechatPrivateKey, NotifyURL: cfg.WechatNotifyURL,
	})
	if err != nil {
		return nil, err
	}

	gateway, err := gatewayclient.NewClient(serviceclient.Client{HTTP: r.InternalHTTP}, cfg.GatewayInternalURL, cfg.ServiceToken)
	if err != nil {
		return nil, err
	}
	router := newRouter(cfg, r, channel, gateway)
	log.Printf("central: http=%s payment=%s gateway=%s", cfg.HTTPAddr, cfg.PaymentMode, cfg.GatewayInternalURL)
	return &application{
		Server: &http.Server{
			Addr:              cfg.HTTPAddr,
			Handler:           router,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       90 * time.Second,
		},
	}, nil
}

// Run 启动 HTTP 服务，停机宽限为十秒；超时取消请求后仍等待处理器退出。
func (a *application) Run(ctx context.Context) error {
	return httpapi.Serve(ctx, a.Server, 10*time.Second)
}
