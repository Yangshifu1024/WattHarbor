package main

import (
	"context"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/card"
	chargingquery "github.com/ChargePilot2026/charge-pilot/internal/charging/query"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	usercard "github.com/ChargePilot2026/charge-pilot/internal/http/user/card"
	usercharging "github.com/ChargePilot2026/charge-pilot/internal/http/user/charging"
	userdevice "github.com/ChargePilot2026/charge-pilot/internal/http/user/device"
	useridentity "github.com/ChargePilot2026/charge-pilot/internal/http/user/identity"
	usermarketing "github.com/ChargePilot2026/charge-pilot/internal/http/user/marketing"
	userops "github.com/ChargePilot2026/charge-pilot/internal/http/user/ops"
	userorder "github.com/ChargePilot2026/charge-pilot/internal/http/user/order"
	userpayment "github.com/ChargePilot2026/charge-pilot/internal/http/user/payment"
	usersettlement "github.com/ChargePilot2026/charge-pilot/internal/http/user/settlement"
	userwallet "github.com/ChargePilot2026/charge-pilot/internal/http/user/wallet"
	"github.com/ChargePilot2026/charge-pilot/internal/identity"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
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
	useridentity.API{Service: identity.SessionService{WeChat: loginExchanger, Users: identity.UserRepo{DB: orm}, Sessions: identity.SessionRepo{Redis: cache}, Tokens: identity.JWTAccess{JWT: jwt}}}.Register(router)
	userpayment.ScanAPI{Discovery: discovery}.Register(router)
	usercharging.StopAPI{JWT: jwt, Sessions: identity.SessionRepo{Redis: cache}, Users: identity.UserRepo{DB: orm}, Gateway: stopGateway}.Register(router)
	// 支付回调由渠道服务器经对外入口打进来，属于本进程。
	if cfg.PaymentMode == "simulation" {
		userpayment.DevelopmentAPI{Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}, Service: payment.SimulationService{DB: orm, Callback: payment.CallbackService{Refunds: settlement.RefundRecorder{}, DB: orm, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: cfg.WeChatAppID}}}.Register(router)
	}
	if cfg.PaymentMode == "wechat_direct" {
		userpayment.WechatCallbackAPI{Verifier: channel.Prepay.(userpayment.NotificationVerifier), Callbacks: payment.CallbackService{Refunds: settlement.RefundRecorder{},
			DB: orm, ExpectedProvider: "wechat_direct", ExpectedMerchantID: cfg.WechatMchID, ExpectedAppID: cfg.WeChatAppID}}.Register(router)
	}
	userwallet.API{WalletQuery: wallet.QueryService{Repo: wallet.UserWalletRepo{DB: orm}}, WalletRecharge: wallet.RechargeService{DB: orm, Payments: payment.RechargePaymentRepo{DB: orm, Provider: channel.Prepay}}, WalletRefund: wallet.RefundService{DB: orm, Sources: payment.RechargeSources{}, Recorder: settlement.RefundRecorder{}}, Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}}.Register(router)
	userdevice.API{Stations: device.UserStationService{DB: orm, Announcements: ops.AnnouncementQueryService{DB: orm}, Summaries: stopGateway}, Faults: device.FaultService{DB: orm}, Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}}.Register(router)
	userops.API{Announcements: ops.AnnouncementQueryService{DB: orm}}.Register(router)
	usermarketing.API{Query: marketing.CouponQueryService{DB: orm}, Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}}.Register(router)
	usersettlement.InvoiceAPI{Invoices: settlement.InvoiceService{DB: orm}, Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}}.Register(router)
	userorder.FeedbackAPI{Feedback: ordercase.FeedbackService{DB: orm}, Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}}.Register(router)
	orderQuery := ordercase.QueryService{DB: orm, Fees: settlement.QueryFeeRepo{DB: orm}}
	userorder.QueryAPI{Orders: orderQuery, Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}, Query: chargingquery.UserService{Orders: orderQuery, Fees: settlement.QueryFeeRepo{DB: orm}, State: chargingquery.UserStateRepo{DB: orm}, Gateway: stopGateway}}.Register(router)
	var phoneSource identity.PhoneSource = identity.WeChatPhoneSource{Exchange: wechat.ExchangePhone}
	if cfg.LoginMode == "development" {
		phoneSource = identity.DevelopmentPhoneSource{}
	}
	useridentity.PhoneAPI{Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}, Service: identity.PhoneService{Users: identity.UserRepo{DB: orm}, Source: phoneSource}}.Register(router)
	usersettlement.BillAPI{Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}, Bills: settlement.UserBillService{Repo: settlement.BillRepo{DB: orm}}}.Register(router)

	userpayment.StartAPI{Auth: authhttp.SessionAuthenticator{Auth: cEndAuth},
		Checkout: payment.CheckoutService{Ports: discovery, Pricing: pricing.Repo{DB: orm}, Intents: payment.PaymentIntentRepo{DB: orm}, Provider: channel.Prepay}}.Register(router)
	usercard.API{Auth: authhttp.SessionAuthenticator{Auth: cEndAuth}, Status: card.CardStatusService{DB: orm}}.Register(router)

	return router
}
