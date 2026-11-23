package user

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"chargepilot/internal/identity"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/config"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"github.com/go-pay/wechat-sdk/mini"
	"github.com/go-pay/xhttp"
)

// Run 启动兼容的独立进程。
func Run(ctx context.Context) (err error) {
	app, close, err := Prepare(ctx, nil, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, close()) }()
	return app.Run(ctx)
}

// Prepare 装配应用和资源；非空 internalHTTP 用于同进程内部调用。
func Prepare(ctx context.Context, internalHTTP *http.Client, notify func()) (_ *Application, _ func() error, err error) {
	cfg, err := config.LoadUser()
	if err != nil {
		return nil, nil, err
	}
	if internalHTTP != nil {
		cfg.GatewayInternalURL = "http://gateway"
	}
	resources, err := openResources(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	if internalHTTP != nil {
		resources.InternalHTTP.CloseIdleConnections()
		resources.InternalHTTP = internalHTTP

	}
	resources.Notify = notify
	app, err := assemble(ctx, cfg, resources)
	if err != nil {
		return nil, nil, errors.Join(err, resources.Close())
	}
	return app, resources.Close, nil
}

// application 持有已组装的 HTTP 服务，资源由 run 释放。
type Application struct {
	// Server 已装配的 HTTP 服务器，由 httpapi.Serve 独占管理监听、请求 context 与退出等待。
	Server *http.Server
}

// assemble 创建会话签名、微信 SDK、支付渠道、网关客户端和路由。
// 微信请求超时为五秒；这里只组装依赖，资源由 run 释放。
func assemble(ctx context.Context, cfg config.User, r *resources) (*Application, error) {
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

	gateway, err := gatewayclient.NewClient(serviceclient.Client{HTTP: r.InternalHTTP}, cfg.GatewayInternalURL, cfg.ServiceToken)
	if err != nil {
		return nil, err
	}
	router := newRouter(cfg, r, jwt, identity.MiniProgram{SDK: wechat}, channel, gateway)
	log.Printf("user: http=%s login=%s payment=%s gateway=%s", cfg.HTTPAddr, cfg.LoginMode, cfg.PaymentMode, cfg.GatewayInternalURL)
	return &Application{
		Server: &http.Server{
			Addr:              cfg.HTTPAddr,
			Handler:           router,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       90 * time.Second,
		},
	}, nil
}

// Run 启动 HTTP 服务，停机宽限为十秒；超时取消请求后仍等待处理器退出。
func (a *Application) Run(ctx context.Context) error {
	return httpapi.Serve(ctx, a.Server, 10*time.Second)
}
