package main

import (
	"context"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/application"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/admin"
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
	cfg, err := config.LoadCentral()
	if err != nil {
		return err
	}
	db, err := dbconn.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	userORM, err := dbconn.WrapGORM(db)
	if err != nil {
		return err
	}
	adminDB := db
	adminORM, billingORM := userORM, userORM
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
	gin.SetMode(gin.ReleaseMode)
	// 在注册路由前安装指标中间件，覆盖全部 HTTP 请求。
	// 指标端点无需运营会话，仅暴露路由模板、状态码与耗时。
	metrics := httpapi.NewMetrics("central")
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
		httpapi.OK(c, gin.H{"status": "identity_storage_ready"})
	})
	adminStore := identitystore.AdminStore{DB: adminORM}
	if err := adminStore.Bootstrap(ctx, cfg.AdminBootstrapUser, cfg.AdminBootstrapPassword); err != nil {
		return err
	}
	// C 端鉴权规则在 identity/user 包，此处只装配一次供全部 C 端接口复用。
	cEndAuth := identityhttp.NewAuthenticator(jwt, sessionstore.Sessions{Redis: cache}, identitystore.UserStore{DB: userORM})
	adminAPI := identityhttp.AdminAuth{Store: adminStore, Sessions: sessionstore.AdminSessions{Redis: cache}, JWT: jwt}
	adminAPI.Register(router)
	admin.RegulatoryEventsAPI{Queue: admin.RegulatoryQueue{AdminDB: adminDB}, ServiceToken: cfg.ServiceToken}.Register(router)
	admin.DeliveryDispatchAPI{Webhook: admin.WebhookDispatch{AdminDB: adminORM}, Regulatory: admin.RegulatoryDispatch{AdminDB: adminORM}, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.Service{Store: identitystore.Store{DB: billingORM}, Orders: identitystore.BillingOrders{DB: userORM}, Splits: identitystore.SplitResolver{AdminDB: adminORM}, ServiceToken: cfg.ServiceToken,
		Bills: identityhttp.BillStoreIssuer{Store: identityhttp.BillStore{DB: userORM}}}.Register(router)
	admin.ResourceAPI{Store: admin.ResourceStore{AdminDB: adminORM, UserDB: userORM, BillingDB: billingORM}, Auth: adminAPI, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken, ExportDir: os.Getenv("EXPORT_DIR")}.Register(router)
	exportCleanup := admin.ExportTask{Store: admin.ResourceStore{AdminDB: adminORM}, ExportDir: os.Getenv("EXPORT_DIR")}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if count, err := exportCleanup.CleanupExpired(ctx); err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("export cleanup: %v", err)
				}
			} else if count > 0 {
				log.Printf("expired %d export files", count)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	admin.Dashboard{UserDB: userORM, AdminDB: adminORM}.Register(router, adminAPI)
	var loginExchanger identityauth.CodeExchanger = identityauth.MiniProgram{SDK: wechat}
	if cfg.LoginMode == "development" {
		loginExchanger = identityauth.DevelopmentExchanger{}
	}
	identityhttp.API{WeChat: loginExchanger, Users: identitystore.UserStore{DB: userORM}, Sessions: sessionstore.Sessions{Redis: cache}, JWT: jwt}.Register(router)
	identityhttp.StartAuthorization{DB: userORM, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.StartResultAPI{Store: identityhttp.StartResultStore{DB: userORM}, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.EndResultAPI{Store: identityhttp.EndResultStore{DB: userORM}, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.StopAuthorization{DB: userORM, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.UserStopAPI{JWT: jwt, Sessions: sessionstore.Sessions{Redis: cache}, Users: identitystore.UserStore{DB: userORM}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.ScanAPI{Operations: identitystore.DeviceOperationStore{DB: adminORM}, Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}.Register(router)
	var prepay identityhttp.PrepayProvider
	var refundProvider identitystore.RefundProvider
	switch cfg.PaymentMode {
	case "simulation":
		prepay = identitystore.Simulator{}
		refundProvider = identitystore.Simulator{}
		identityhttp.SimulationCallbackAPI{DB: userORM, Store: identitystore.PaymentCallbackStore{DB: userORM, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: cfg.WeChatAppID}, ServiceToken: cfg.ServiceToken}.Register(router)
		identityhttp.DevelopmentPaymentAPI{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth}, DB: userORM, Store: identitystore.PaymentCallbackStore{DB: userORM, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: cfg.WeChatAppID}}.Register(router)
	case "wechat_direct":
		direct, err := identitystore.NewWechatDirect(ctx, identitystore.Config{AppID: cfg.WeChatAppID, MerchantID: cfg.WechatMchID,
			CertificateSerial: cfg.WechatCertSerial, APIv3Key: cfg.WechatAPIv3Key,
			PrivateKeyPath: cfg.WechatPrivateKey, NotifyURL: cfg.WechatNotifyURL})
		if err != nil {
			return err
		}
		prepay = direct
		refundProvider = direct
		identityhttp.WechatCallbackAPI{Verifier: direct, Store: identitystore.PaymentCallbackStore{DB: userORM, ExpectedProvider: "wechat_direct", ExpectedMerchantID: cfg.WechatMchID, ExpectedAppID: cfg.WeChatAppID}}.Register(router)
	}
	identityhttp.RefundAPI{Executor: identitystore.RefundExecutor{DB: userORM, Provider: refundProvider, ProviderName: cfg.PaymentMode}, ServiceToken: cfg.ServiceToken}.Register(router)
	application.AutoStopAPI{Stopper: application.AutoStopper{UserDB: userORM, Gateway: serviceclient.Client{Timeout: 10 * time.Second}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}, ServiceToken: cfg.ServiceToken}.Register(router)
	admin.DeviceAlertSyncAPI{Sync: admin.DeviceAlertSync{AdminDB: adminORM, Gateway: serviceclient.Client{Timeout: 10 * time.Second}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.UserAccountAPI{
		Auth:   identityhttp.SessionAuthenticator{Auth: cEndAuth},
		UserDB: userORM, AdminDB: adminORM, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken,
		Gateway: serviceclient.Client{Timeout: 8 * time.Second}, Prepay: prepay,
	}.Register(router)
	identityhttp.PhoneAPI{
		Auth:             identityhttp.SessionAuthenticator{Auth: cEndAuth},
		Users:            identitystore.UserStore{DB: userORM},
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
	identityhttp.BillHTTP{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth},
		Bills: identityhttp.BillStore{DB: userORM}}.Register(router)
	identityhttp.UserQueryAPI{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth},
		DB: userORM, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken, Gateway: serviceclient.Client{Timeout: 8 * time.Second}}.Register(router)
	identityhttp.CouponAPI{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth},
		Coupons: identitystore.CouponStore{DB: userORM}}.Register(router)
	identityhttp.PaymentStartAPI{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth},
		Scan: identityhttp.ScanAPI{Operations: identitystore.DeviceOperationStore{DB: adminORM}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}, Pricing: identitystore.PricingStore{DB: adminORM},
		Intents: identitystore.PaymentIntentStore{DB: userORM}, Provider: prepay}.Register(router)
	identityhttp.CardAPI{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth}, Store: identitystore.CardStore{DB: userORM}, Pricing: identitystore.PricingStore{DB: adminORM}, Scan: identityhttp.ScanPortLookup{API: identityhttp.ScanAPI{Operations: identitystore.DeviceOperationStore{DB: adminORM}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}}, ServiceToken: cfg.ServiceToken}.Register(router)
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
