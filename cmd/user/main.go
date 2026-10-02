// 命令 user 是 C 端进程：只暴露 /api/v1/user/* 与支付回调，不含任何服务间接口。
//
// 充电过程中的实时读数（遥测、历史曲线、充电采样、设备摘要）由 gateway 的
// /api/v1/internal/devices/* 提供，这里经 GATEWAY_INTERNAL_URL 携服务令牌回调。
// 这些端点不在 central 进程里，改动时不要按路径里的 /internal 误判调用对象。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	identityauth "github.com/ChargePilot2026/charge-pilot/internal/domain/identity/auth"
	identitystore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
	sessionstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/redis"
	identityhttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"github.com/go-pay/wechat-sdk/mini"
	"github.com/go-pay/xhttp"
	"github.com/redis/go-redis/v9"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadUser()
	if err != nil {
		return err
	}
	db, err := dbconn.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		return err
	}
	redisOptions, err := redis.ParseURL(cfg.RedisCacheURL)
	if err != nil {
		return err
	}
	cache := redis.NewClient(redisOptions)
	defer cache.Close()
	if err := cache.Ping(ctx).Err(); err != nil {
		return err
	}
	jwt, err := auth.NewJWT(cfg.JWTSecret)
	if err != nil {
		return err
	}
	wechat, err := mini.New(cfg.WeChatAppID, cfg.WeChatAppSecret, false)
	if err != nil {
		return err
	}
	wechat.SetHttpClient(xhttp.NewClient().SetTimeout(5 * time.Second))
	channel, err := identitystore.NewPaymentChannel(ctx, cfg.PaymentMode, identitystore.WechatCredentials{
		AppID: cfg.WeChatAppID, MerchantID: cfg.WechatMchID,
		CertificateSerial: cfg.WechatCertSerial, APIv3Key: cfg.WechatAPIv3Key,
		PrivateKeyPath: cfg.WechatPrivateKey, NotifyURL: cfg.WechatNotifyURL,
	})
	if err != nil {
		return err
	}
	gin.SetMode(gin.ReleaseMode)
	metrics := httpapi.NewMetrics("user")
	router := httpapi.NewRouter(metrics)
	metrics.Register(router)
	router.GET("/health/live", func(c *gin.Context) { httpapi.OK(c, gin.H{"status": "live"}) })
	router.GET("/health/ready", func(c *gin.Context) {
		check, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if db.PingContext(check) != nil || cache.Ping(check).Err() != nil {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "storage unavailable", nil)
			return
		}
		httpapi.OK(c, gin.H{"status": "user_storage_ready"})
	})

	cEndAuth := identityhttp.NewAuthenticator(jwt, sessionstore.Sessions{Redis: cache}, identitystore.UserStore{DB: orm})
	var loginExchanger identityauth.CodeExchanger = identityauth.MiniProgram{SDK: wechat}
	if cfg.LoginMode == "development" {
		loginExchanger = identityauth.DevelopmentExchanger{}
	}
	identityhttp.API{WeChat: loginExchanger, Users: identitystore.UserStore{DB: orm}, Sessions: sessionstore.Sessions{Redis: cache}, JWT: jwt}.Register(router)
	identityhttp.ScanAPI{Operations: identitystore.DeviceOperationStore{DB: orm}, Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.UserStopAPI{JWT: jwt, Sessions: sessionstore.Sessions{Redis: cache}, Users: identitystore.UserStore{DB: orm}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}.Register(router)
	// 支付回调由渠道服务器经对外入口打进来，属于本进程。
	if cfg.PaymentMode == "simulation" {
		identityhttp.DevelopmentPaymentAPI{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth}, DB: orm,
			Store: identitystore.PaymentCallbackStore{DB: orm, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: cfg.WeChatAppID}}.Register(router)
	}
	if cfg.PaymentMode == "wechat_direct" {
		identityhttp.WechatCallbackAPI{Verifier: channel.Prepay.(identityhttp.NotificationVerifier), Store: identitystore.PaymentCallbackStore{
			DB: orm, ExpectedProvider: "wechat_direct", ExpectedMerchantID: cfg.WechatMchID, ExpectedAppID: cfg.WeChatAppID}}.Register(router)
	}
	identityhttp.UserAccountAPI{
		Auth:   identityhttp.SessionAuthenticator{Auth: cEndAuth},
		UserDB: orm, AdminDB: orm, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken,
		Gateway: serviceclient.Client{Timeout: 8 * time.Second}, Prepay: channel.Prepay,
	}.Register(router)
	identityhttp.UserQueryAPI{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth},
		DB: orm, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken, Gateway: serviceclient.Client{Timeout: 8 * time.Second}}.Register(router)
	identityhttp.PhoneAPI{
		Auth:             identityhttp.SessionAuthenticator{Auth: cEndAuth},
		Users:            identitystore.UserStore{DB: orm},
		DevelopmentPhone: cfg.LoginMode == "development",
		PhoneExchange: func(ctx context.Context, code string) (string, error) {
			result, err := wechat.GetPhoneNumber(ctx, code)
			if err != nil {
				return "", err
			}
			if result == nil || result.Errcode != 0 || result.PhoneInfo == nil {
				return "", errors.New("invalid phone authorization")
			}
			return result.PhoneInfo.PurePhoneNumber, nil
		},
	}.Register(router)
	identityhttp.BillHTTP{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth}, Bills: identityhttp.BillStore{DB: orm}}.Register(router)
	identityhttp.CouponAPI{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth}, Coupons: identitystore.CouponStore{DB: orm}}.Register(router)
	identityhttp.PaymentStartAPI{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth},
		Scan:     identityhttp.ScanAPI{Operations: identitystore.DeviceOperationStore{DB: orm}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken},
		Pricing:  identitystore.PricingStore{DB: orm},
		Intents:  identitystore.PaymentIntentStore{DB: orm},
		Provider: channel.Prepay}.Register(router)
	identityhttp.CardAPI{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth}, Store: identitystore.CardStore{DB: orm}, Pricing: identitystore.PricingStore{DB: orm},
		Scan:         identityhttp.ScanPortLookup{API: identityhttp.ScanAPI{Operations: identitystore.DeviceOperationStore{DB: orm}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}},
		ServiceToken: cfg.ServiceToken}.RegisterUser(router)

	server := &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServe(); errors.Is(err, http.ErrServerClosed) {
		return nil
	} else {
		return err
	}
}
