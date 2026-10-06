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

// newRouter 注册当前进程路由、指标与健康端点；cfg 提供登录与支付模式，r 提供已打开存储，jwt 提供会话签名，wechat 提供微信能力，channel 提供支付渠道，stopGateway 提供设备内部调用。
// 返回未监听的 Gin 引擎，无错误返回；设置全局 Gin 发布模式，仅装配不执行业务请求，健康处理器用请求 context 的两秒子期限检查存储。
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

	// 会话与档案仓储、请求鉴权器均为无状态值服务，本进程内共用同一实例；
	// sessionAuth 仅封装 cEndAuth，语义与逐处构造一致。
	sessionRepo := identity.SessionRepo{Redis: cache}
	userRepo := identity.UserRepo{DB: orm}
	cEndAuth := authhttp.NewAuthenticator(jwt, sessionRepo, userRepo)
	sessionAuth := authhttp.SessionAuthenticator{Auth: cEndAuth}
	discovery := device.DiscoveryService{Gateway: stopGateway, Operations: device.OperationRepo{DB: orm}}
	// 登录模式同时决定微信换据与手机号来源采用真实实现还是开发模拟。
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
