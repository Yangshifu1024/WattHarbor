package main

import (
	"context"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/card"
	"github.com/ChargePilot2026/charge-pilot/internal/identity"
	sessionstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/redis"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	identityhttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/marketing"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/gin-gonic/gin"
)

func newRouter(cfg config.User, r *resources, jwt *auth.JWT, wechat identity.MiniProgram, channel payment.PaymentChannel, stopGateway *gatewayclient.Client) *gin.Engine {
	db, orm, cache := r.DB, r.ORM, r.Cache
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

	cEndAuth := identityhttp.NewAuthenticator(jwt, sessionstore.Sessions{Redis: cache}, identity.UserStore{DB: orm})
	var loginExchanger identity.CodeExchanger = wechat
	if cfg.LoginMode == "development" {
		loginExchanger = identity.DevelopmentExchanger{}
	}
	identityhttp.API{WeChat: loginExchanger, Users: identity.UserStore{DB: orm}, Sessions: sessionstore.Sessions{Redis: cache}, JWT: jwt}.Register(router)
	identityhttp.ScanAPI{Operations: payment.DeviceOperationStore{DB: orm}, Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.UserStopAPI{JWT: jwt, Sessions: sessionstore.Sessions{Redis: cache}, Users: identity.UserStore{DB: orm}, Gateway: stopGateway}.Register(router)
	// 支付回调由渠道服务器经对外入口打进来，属于本进程。
	if cfg.PaymentMode == "simulation" {
		identityhttp.DevelopmentPaymentAPI{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth}, DB: orm,
			Store: payment.PaymentCallbackStore{DB: orm, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: cfg.WeChatAppID}}.Register(router)
	}
	if cfg.PaymentMode == "wechat_direct" {
		identityhttp.WechatCallbackAPI{Verifier: channel.Prepay.(identityhttp.NotificationVerifier), Store: payment.PaymentCallbackStore{
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
		Users:            identity.UserStore{DB: orm},
		DevelopmentPhone: cfg.LoginMode == "development",
		PhoneExchange:    wechat.ExchangePhone,
	}.Register(router)
	identityhttp.BillHTTP{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth}, Bills: identityhttp.BillStore{DB: orm}}.Register(router)
	identityhttp.CouponAPI{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth}, Coupons: marketing.CouponStore{DB: orm}}.Register(router)
	identityhttp.PaymentStartAPI{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth},
		Scan:     identityhttp.ScanAPI{Operations: payment.DeviceOperationStore{DB: orm}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken},
		Pricing:  pricing.PricingStore{DB: orm},
		Intents:  payment.PaymentIntentStore{DB: orm},
		Provider: channel.Prepay}.Register(router)
	identityhttp.CardAPI{Auth: identityhttp.SessionAuthenticator{Auth: cEndAuth}, Store: card.CardStore{DB: orm}, Pricing: pricing.PricingStore{DB: orm},
		Scan:         identityhttp.ScanPortLookup{API: identityhttp.ScanAPI{Operations: payment.DeviceOperationStore{DB: orm}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}},
		ServiceToken: cfg.ServiceToken}.RegisterUser(router)

	return router
}
