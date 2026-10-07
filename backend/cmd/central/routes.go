package main

import (
	"context"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/card"
	chargefinish "github.com/ChargePilot2026/charge-pilot/internal/charging/finish"
	chargestart "github.com/ChargePilot2026/charge-pilot/internal/charging/start"
	chargestop "github.com/ChargePilot2026/charge-pilot/internal/charging/stop"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	centralhttp "github.com/ChargePilot2026/charge-pilot/internal/http/central"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/gin-gonic/gin"
)

// newRouter 注册当前进程路由、指标与健康端点；cfg 提供服务令牌与支付模式，r 提供已打开存储，channel 提供退款渠道，gateway 提供设备内部调用。
// 返回未监听的 Gin 引擎，无错误返回；设置全局 Gin 发布模式，仅装配不执行业务请求，健康处理器用请求 context 的两秒子期限检查存储。
func newRouter(cfg config.Central, r *resources, channel payment.PaymentChannel, gateway *gatewayclient.Client) *gin.Engine {
	db, orm, cache := r.DB, r.ORM, r.Cache
	gin.SetMode(gin.ReleaseMode)
	// 指标端点无需会话，只暴露路由模板、状态码与耗时。
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
	// 设备操作仓储为无状态值服务，卡Swipe的端口探测与设备事实共用同一实例。
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

	// 资金与账单：开票、结算、退款派发。
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
