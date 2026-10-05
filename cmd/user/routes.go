package main

import (
	"context"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/card"
	chargingquery "github.com/ChargePilot2026/charge-pilot/internal/charging/query"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/identity"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	identityhttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/marketing"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	ordercase "github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
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

	discovery := device.DiscoveryService{Gateway: stopGateway, Operations: device.OperationRepo{DB: orm}}
	cEndAuth := authhttp.NewAuthenticator(jwt, identity.SessionRepo{Redis: cache}, identity.UserRepo{DB: orm})
	var loginExchanger identity.CodeExchanger = wechat
	if cfg.LoginMode == "development" {
		loginExchanger = identity.DevelopmentExchanger{}
	}
	identityhttp.API{Service: identity.SessionService{WeChat: loginExchanger, Users: identity.UserRepo{DB: orm}, Sessions: identity.SessionRepo{Redis: cache}, Tokens: identity.JWTAccess{JWT: jwt}}}.Register(router)
	identityhttp.ScanAPI{Discovery: discovery}.Register(router)
	identityhttp.UserStopAPI{JWT: jwt, Sessions: identity.SessionRepo{Redis: cache}, Users: identity.UserRepo{DB: orm}, Gateway: stopGateway}.Register(router)
	// 支付回调由渠道服务器经对外入口打进来，属于本进程。
	if cfg.PaymentMode == "simulation" {
		identityhttp.DevelopmentPaymentAPI{Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}, Service: payment.SimulationService{DB: orm, Callback: payment.CallbackService{Refunds: settlement.RefundRecorder{}, DB: orm, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: cfg.WeChatAppID}}}.Register(router)
	}
	if cfg.PaymentMode == "wechat_direct" {
		identityhttp.WechatCallbackAPI{Verifier: channel.Prepay.(identityhttp.NotificationVerifier), Store: payment.CallbackService{Refunds: settlement.RefundRecorder{},
			DB: orm, ExpectedProvider: "wechat_direct", ExpectedMerchantID: cfg.WechatMchID, ExpectedAppID: cfg.WeChatAppID}}.Register(router)
	}
	identityhttp.UserAccountAPI{Stations: device.UserStationService{DB: orm, Announcements: ops.AnnouncementQueryService{DB: orm}, Summaries: stopGateway}, Announcements: ops.AnnouncementQueryService{DB: orm}, Faults: device.FaultService{DB: orm}, CouponQuery: marketing.CouponQueryService{DB: orm}, Feedback: ordercase.FeedbackService{DB: orm}, WalletQuery: wallet.QueryService{Repo: wallet.UserWalletRepo{DB: orm}}, Invoices: settlement.InvoiceService{DB: orm}, WalletRecharge: wallet.RechargeService{DB: orm, Payments: payment.RechargePaymentRepo{DB: orm, Provider: channel.Prepay}}, WalletRefund: wallet.RefundService{DB: orm, Sources: payment.RechargeSources{}, Recorder: settlement.RefundRecorder{}},
		Auth: authhttp.SessionAuthenticator{Auth: cEndAuth},
	}.Register(router)
	orderQuery := ordercase.QueryService{DB: orm, Fees: settlement.QueryFeeRepo{DB: orm}}
	identityhttp.UserQueryAPI{Orders: orderQuery, Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}, Query: chargingquery.UserService{Orders: orderQuery, Fees: settlement.QueryFeeRepo{DB: orm}, State: chargingquery.UserStateRepo{DB: orm}, Gateway: stopGateway}}.Register(router)
	var phoneSource identity.PhoneSource = identity.WeChatPhoneSource{Exchange: wechat.ExchangePhone}
	if cfg.LoginMode == "development" {
		phoneSource = identity.DevelopmentPhoneSource{}
	}
	identityhttp.PhoneAPI{Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}, Service: identity.PhoneService{Users: identity.UserRepo{DB: orm}, Source: phoneSource}}.Register(router)
	identityhttp.BillHTTP{Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}, Bills: settlement.UserBillService{Repo: settlement.BillRepo{DB: orm}}}.Register(router)
	identityhttp.CouponAPI{Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}, Query: marketing.CouponQueryService{DB: orm}}.Register(router)
	identityhttp.PaymentStartAPI{Auth: authhttp.SessionAuthenticator{Auth: cEndAuth},
		Checkout: payment.CheckoutService{Ports: discovery, Pricing: pricing.Repo{DB: orm}, Intents: payment.PaymentIntentRepo{DB: orm}, Provider: channel.Prepay}}.Register(router)
	identityhttp.CardAPI{Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}, Status: card.CardStatusService{DB: orm}}.RegisterUser(router)

	return router
}
