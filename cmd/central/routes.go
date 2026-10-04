package main

import (
	"context"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/card"
	chargefinish "github.com/ChargePilot2026/charge-pilot/internal/charging/finish"
	chargestart "github.com/ChargePilot2026/charge-pilot/internal/charging/start"
	chargestop "github.com/ChargePilot2026/charge-pilot/internal/charging/stop"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	identityhttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	adminalertsync "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/alertsync"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/gin-gonic/gin"
)

func newRouter(cfg config.Central, r *resources, channel payment.PaymentChannel, gateway *gatewayclient.Client) *gin.Engine {
	db, orm, cache := r.DB, r.ORM, r.Cache
	gin.SetMode(gin.ReleaseMode)
	// 指标端点无需会话，只暴露路由模板、状态码与耗时。
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
		httpapi.OK(c, gin.H{"status": "central_storage_ready"})
	})

	// 充电生命周期：gateway 查询授权，worker 查询待启动订单并同步回执。
	identityhttp.PaidStartsAPI{Service: chargestart.PendingOrders{DB: orm}, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.StartAuthorization{Service: chargestart.AuthorizationService{DB: orm}, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.StartResultAPI{Service: chargestart.StartResultService{DB: orm}, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.EndResultAPI{Service: chargefinish.EndResultService{DB: orm}, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.StopAuthorization{Service: chargestop.AuthorizationService{DB: orm}, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.CardAPI{Store: card.CardStore{DB: orm}, Pricing: pricing.PricingStore{DB: orm},
		ServiceToken: cfg.ServiceToken}.RegisterInternal(router)
	identityhttp.AutoStopAPI{Stopper: chargestop.AutoStopper{DB: orm, Gateway: gateway}, ServiceToken: cfg.ServiceToken}.Register(router)

	// 资金与账单：开票、结算、退款派发。
	identityhttp.Service{Store: settlement.Store{DB: orm}, Orders: settlement.BillingOrders{DB: orm}, ServiceToken: cfg.ServiceToken,
		Bills: identityhttp.BillStoreIssuer{Store: identityhttp.BillStore{DB: orm}}}.Register(router)
	identityhttp.RefundAPI{Executor: settlement.RefundExecutor{DB: orm, Provider: channel.Refund, ProviderName: channel.Name}, ServiceToken: cfg.ServiceToken}.Register(router)
	if cfg.PaymentMode == "simulation" {
		identityhttp.SimulationCallbackAPI{DB: orm, Store: payment.PaymentCallbackStore{
			DB: orm, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: cfg.WeChatAppID}, ServiceToken: cfg.ServiceToken}.Register(router)
	}

	// 设备告警同步。
	adminalertsync.DeviceAlertSyncAPI{Sync: adminalertsync.DeviceAlertSync{AdminDB: orm, Gateway: serviceclient.Client{HTTP: r.InternalHTTP}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}, ServiceToken: cfg.ServiceToken}.Register(router)

	return router
}
