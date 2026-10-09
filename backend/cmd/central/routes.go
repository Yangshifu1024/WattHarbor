package main

import (
	"context"
	"net/http"
	"time"

	"chargepilot/internal/card"
	chargefinish "chargepilot/internal/charging/finish"
	chargestart "chargepilot/internal/charging/start"
	chargestop "chargepilot/internal/charging/stop"
	"chargepilot/internal/device"
	centralhttp "chargepilot/internal/http/central"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/config"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/pricing"
	"chargepilot/internal/settlement"
	"github.com/gin-gonic/gin"
)

// newRouter 注册本进程的业务路由、指标和健康检查，不启动监听。
// 健康检查使用请求 context，最多等待两秒。
func newRouter(cfg config.Central, r *resources, channel payment.PaymentChannel, gateway *gatewayclient.Client) *gin.Engine {
	db, orm, cache := r.DB, r.ORM, r.Cache
	gin.SetMode(gin.ReleaseMode)
	// /metrics 无需登录，包含请求、进程和数据库连接池统计。
	metrics := httpapi.NewMetrics("central", httpapi.DatabasePool{Schema: "central_db", DB: r.DB})
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

	// 内部端点统一使用同一服务令牌：内部调用只认 X-Service-Token，不暴露到前端。
	token := cfg.ServiceToken
	// 刷卡前查询端口及设备的运营状态，共用此仓储。
	operationRepo := device.OperationRepo{DB: orm}

	// 充电生命周期：gateway 查询授权，worker 查询待启动订单并同步回执。
	centralhttp.PaidStartsAPI{Service: chargestart.PendingOrders{DB: orm}, ServiceToken: token}.Register(router)
	centralhttp.StartAuthorization{Service: chargestart.AuthorizationService{DB: orm}, ServiceToken: token}.Register(router)
	centralhttp.StartResultAPI{Service: chargestart.StartResultService{DB: orm}, ServiceToken: token}.Register(router)
	centralhttp.EndResultAPI{Service: chargefinish.EndResultService{DB: orm}, ServiceToken: token}.Register(router)
	centralhttp.StopAuthorization{Service: chargestop.AuthorizationService{DB: orm}, ServiceToken: token}.Register(router)
	centralhttp.CardAPI{
		Swipe: card.SwipeService{
			DB:      orm,
			Ports:   device.DiscoveryService{Gateway: gateway, Operations: operationRepo},
			Devices: operationRepo,
			Pricing: pricing.Repo{DB: orm},
			Cutoff:  chargestop.CutoffRepo{},
		},
		Status:       card.CardStatusService{DB: orm},
		ServiceToken: token,
	}.RegisterInternal(router)
	centralhttp.AutoStopAPI{Stopper: chargestop.AutoStopper{DB: orm, Gateway: gateway}, ServiceToken: token}.Register(router)

	// 计费结算与退款执行。
	centralhttp.BillingAPI{
		Service: settlement.BillingService{
			Calculator: settlement.FeeCalculator{DB: orm},
			Orders:     settlement.BillingOrders{DB: orm},
			Bills:      settlement.BillRepo{DB: orm},
		},
		ServiceToken: token,
	}.Register(router)
	centralhttp.RefundAPI{
		Executor: settlement.RefundExecutor{
			DB:           orm,
			Provider:     channel.Refund,
			ProviderName: channel.Name,
		},
		ServiceToken: token,
	}.Register(router)
	if cfg.PaymentMode == "simulation" {
		centralhttp.SimulationCallbackAPI{
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
			ServiceToken: token,
		}.Register(router)
	}

	// 设备告警同步。
	centralhttp.DeviceAlertSyncAPI{
		Sync:         device.AlertSyncService{DB: orm, Gateway: gateway},
		ServiceToken: token,
	}.Register(router)

	return router
}
