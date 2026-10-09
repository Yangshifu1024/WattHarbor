package main

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

// run 接收受信号管理的 ctx，加载当前进程配置、打开资源并装配应用；ctx 用于初始化与应用取消。
// 返回配置、打开、装配或运行错误，并合并资源关闭错误；打开失败由打开方清理，应用全部退出后才关闭资源，无独立后台任务。
func run(ctx context.Context) (err error) {
	cfg, err := config.LoadUser()
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

// application 持有已装配的 HTTP 服务，不另建资源释放流程。
type application struct {
	// Server 已装配的 HTTP 服务器，由 httpapi.Serve 独占管理监听、请求 context 与退出等待。
	Server *http.Server
}

// assemble 接收初始化 ctx、C 端配置 cfg 与已打开的 r，创建 JWT、微信 SDK、支付渠道、网关客户端及路由。
// 返回尚未启动的 application，任一依赖配置失败返回 nil 与原错误；微信 SDK 使用五秒超时，不启动受管理循环，资源仍由 run 释放。
func assemble(ctx context.Context, cfg config.User, r *resources) (*application, error) {
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
	return &application{
		Server: &http.Server{
			Addr:              cfg.HTTPAddr,
			Handler:           router,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       90 * time.Second,
		},
	}, nil
}

// Run 接收进程 ctx，以十秒宽限运行 a.Server；取消或监听失败后等待受管理请求退出。
// 返回监听、运行与停机错误；正常取消为 nil，宽限超时会取消请求但仍等待处理器，不在本方法关闭存储或客户端。
func (a *application) Run(ctx context.Context) error {
	return httpapi.Serve(ctx, a.Server, 10*time.Second)
}
