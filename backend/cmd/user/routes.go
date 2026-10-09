package main

import (
	"context"
	"net/http"
	"time"

	"chargepilot/internal/card"
	chargingquery "chargepilot/internal/charging/query"
	"chargepilot/internal/device"
	authhttp "chargepilot/internal/http/auth"
	usercard "chargepilot/internal/http/user/card"
	usercharging "chargepilot/internal/http/user/charging"
	userdevice "chargepilot/internal/http/user/device"
	useridentity "chargepilot/internal/http/user/identity"
	usermarketing "chargepilot/internal/http/user/marketing"
	userops "chargepilot/internal/http/user/ops"
	userorder "chargepilot/internal/http/user/order"
	userpayment "chargepilot/internal/http/user/payment"
	usersettlement "chargepilot/internal/http/user/settlement"
	userwallet "chargepilot/internal/http/user/wallet"
	"chargepilot/internal/identity"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/marketing"
	"chargepilot/internal/ops"
	ordercase "chargepilot/internal/order"
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/config"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/pricing"
	"chargepilot/internal/settlement"
	"chargepilot/internal/wallet"
	"github.com/gin-gonic/gin"
)

// newRouter 注册本进程的业务路由、指标和健康检查，不启动监听。
// 健康检查使用请求 context，最多等待两秒。
func newRouter(cfg config.User, r *resources, jwt *auth.JWT, wechat identity.MiniProgram, channel payment.PaymentChannel, stopGateway *gatewayclient.Client) *gin.Engine {
	db, orm, cache := r.DB, r.ORM, r.Cache
	gin.SetMode(gin.ReleaseMode)
	metrics := httpapi.NewMetrics("user", httpapi.DatabasePool{Schema: "central_db", DB: r.DB})
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

	// 会话、档案和请求鉴权共用同一组服务实例。
	sessionRepo := identity.SessionRepo{Redis: cache}
	userRepo := identity.UserRepo{DB: orm}
	cEndAuth := authhttp.NewAuthenticator(jwt, sessionRepo, userRepo)
	sessionAuth := authhttp.SessionAuthenticator{Auth: cEndAuth}
	discovery := device.DiscoveryService{Gateway: stopGateway, Operations: device.OperationRepo{DB: orm}}
	// 登录模式决定微信登录和手机号授权使用真实接口还是开发模拟。
	var loginExchanger identity.CodeExchanger = wechat
	if cfg.LoginMode == "development" {
		loginExchanger = identity.DevelopmentExchanger{}
	}
	var phoneSource identity.PhoneSource = identity.WeChatPhoneSource{Exchange: wechat.ExchangePhone}
	if cfg.LoginMode == "development" {
		phoneSource = identity.DevelopmentPhoneSource{}
	}

	// 会话、扫码与充电停止。
	useridentity.API{
		Service: identity.SessionService{
			WeChat:   loginExchanger,
			Users:    userRepo,
			Sessions: sessionRepo,
			Tokens:   identity.JWTAccess{JWT: jwt},
		},
	}.Register(router)
	userpayment.ScanAPI{Discovery: discovery}.Register(router)
	usercharging.StopAPI{JWT: jwt, Sessions: sessionRepo, Users: userRepo, Gateway: stopGateway}.Register(router)
	// 支付回调由渠道服务器经对外入口打进来，属于本进程。
	if cfg.PaymentMode == "simulation" {
		userpayment.DevelopmentAPI{
			Auth: sessionAuth,
			Service: payment.SimulationService{
				DB: orm,
				Callback: payment.CallbackService{
					Refunds:            settlement.RefundRecorder{},
					DB:                 orm,
					ExpectedProvider:   "simulation",
					ExpectedMerchantID: "local-simulation",
					ExpectedAppID:      cfg.WeChatAppID,
				},
			},
		}.Register(router)
	}
	if cfg.PaymentMode == "wechat_direct" {
		userpayment.WechatCallbackAPI{
			Verifier: channel.Prepay.(userpayment.NotificationVerifier),
			Callbacks: payment.CallbackService{
				Refunds:            settlement.RefundRecorder{},
				DB:                 orm,
				ExpectedProvider:   "wechat_direct",
				ExpectedMerchantID: cfg.WechatMchID,
				ExpectedAppID:      cfg.WeChatAppID,
			},
		}.Register(router)
	}

	// 钱包、站点设备与各业务族查询。
	userwallet.API{
		WalletQuery:    wallet.QueryService{Repo: wallet.UserWalletRepo{DB: orm}},
		WalletRecharge: wallet.RechargeService{DB: orm, Payments: payment.RechargePaymentRepo{DB: orm, Provider: channel.Prepay}},
		WalletRefund:   wallet.RefundService{DB: orm, Sources: payment.RechargeSources{}, Recorder: settlement.RefundRecorder{}},
		Auth:           sessionAuth,
	}.Register(router)
	userdevice.API{
		Stations: device.UserStationService{DB: orm, Announcements: ops.AnnouncementQueryService{DB: orm}, Summaries: stopGateway},
		Faults:   device.FaultService{DB: orm},
		Auth:     sessionAuth,
	}.Register(router)
	userops.API{Announcements: ops.AnnouncementQueryService{DB: orm}}.Register(router)
	usermarketing.API{Query: marketing.CouponQueryService{DB: orm}, Auth: sessionAuth}.Register(router)
	usersettlement.InvoiceAPI{Invoices: settlement.InvoiceService{DB: orm}, Auth: sessionAuth}.Register(router)
	userorder.FeedbackAPI{Feedback: ordercase.FeedbackService{DB: orm}, Auth: sessionAuth}.Register(router)
	feeRepo := settlement.QueryFeeRepo{DB: orm}
	orderQuery := ordercase.QueryService{DB: orm, Fees: feeRepo}
	userorder.QueryAPI{
		Orders: orderQuery,
		Auth:   sessionAuth,
		Query: chargingquery.UserService{
			Orders:  orderQuery,
			Fees:    feeRepo,
			State:   chargingquery.UserStateRepo{DB: orm},
			Gateway: stopGateway,
			Live: chargingquery.LiveService{
				Gateway: stopGateway,
				Orders:  ordercase.SnapshotRepo{DB: orm},
				Cards:   card.PurchaseQueryRepo{DB: orm},
			},
		},
	}.Register(router)
	useridentity.PhoneAPI{
		Auth:    sessionAuth,
		Service: identity.PhoneService{Users: userRepo, Source: phoneSource},
	}.Register(router)
	usersettlement.BillAPI{
		Auth:  sessionAuth,
		Bills: settlement.UserBillService{Repo: settlement.BillRepo{DB: orm}},
	}.Register(router)

	// 扫码下单与在线卡。
	userpayment.StartAPI{
		Auth: sessionAuth,
		Checkout: payment.CheckoutService{
			Ports:    discovery,
			Pricing:  pricing.Repo{DB: orm},
			Intents:  payment.PaymentIntentRepo{DB: orm},
			Provider: channel.Prepay,
		},
	}.Register(router)
	usercard.API{Auth: sessionAuth, Status: card.CardStatusService{DB: orm}}.Register(router)

	return router
}
