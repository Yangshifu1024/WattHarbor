// 命令 central 是对内进程：只暴露 /api/v1/internal/*，不直接对外。
//
// 这些端点由 gateway（设备侧回调）、worker（结果回传、投递队列）与 admin
// （webhook 订阅、监管报送）携服务令牌调用。生产编排里本进程不映射端口，
// 对外入口只有 Caddy；开发栈同样不映射，保持隔离策略一致。
package main

import (
	"context"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/application"
	"github.com/ChargePilot2026/charge-pilot/internal/card"
	identityhttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	adminalertsync "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/alertsync"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/gin-gonic/gin"
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
	channel, err := payment.NewPaymentChannel(ctx, cfg.PaymentMode, payment.WechatCredentials{
		AppID: cfg.WeChatAppID, MerchantID: cfg.WechatMchID,
		CertificateSerial: cfg.WechatCertSerial, APIv3Key: cfg.WechatAPIv3Key,
		PrivateKeyPath: cfg.WechatPrivateKey, NotifyURL: cfg.WechatNotifyURL,
	})
	if err != nil {
		return err
	}
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

	// 充电流程：授权、结果回传与停机，全部由 gateway 携服务令牌调用。
	identityhttp.StartAuthorization{DB: orm, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.StartResultAPI{Store: identityhttp.StartResultStore{DB: orm}, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.EndResultAPI{Store: identityhttp.EndResultStore{DB: orm}, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.StopAuthorization{DB: orm, ServiceToken: cfg.ServiceToken}.Register(router)
	identityhttp.CardAPI{Store: card.CardStore{DB: orm}, Pricing: pricing.PricingStore{DB: orm},
		ServiceToken: cfg.ServiceToken}.RegisterInternal(router)
	application.AutoStopAPI{Stopper: application.AutoStopper{UserDB: orm, Gateway: serviceclient.Client{Timeout: 10 * time.Second}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}, ServiceToken: cfg.ServiceToken}.Register(router)

	// 资金与账单：开票、结算、退款派发。
	identityhttp.Service{Store: settlement.Store{DB: orm}, Orders: settlement.BillingOrders{DB: orm}, ServiceToken: cfg.ServiceToken,
		Bills: identityhttp.BillStoreIssuer{Store: identityhttp.BillStore{DB: orm}}}.Register(router)
	identityhttp.RefundAPI{Executor: settlement.RefundExecutor{DB: orm, Provider: channel.Refund, ProviderName: channel.Name}, ServiceToken: cfg.ServiceToken}.Register(router)
	if cfg.PaymentMode == "simulation" {
		identityhttp.SimulationCallbackAPI{DB: orm, Store: payment.PaymentCallbackStore{
			DB: orm, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: cfg.WeChatAppID}, ServiceToken: cfg.ServiceToken}.Register(router)
	}

	// 事件投递与告警同步：webhook 订阅、监管报送、设备告警。
	adminalertsync.DeviceAlertSyncAPI{Sync: adminalertsync.DeviceAlertSync{AdminDB: orm, Gateway: serviceclient.Client{Timeout: 10 * time.Second}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}, ServiceToken: cfg.ServiceToken}.Register(router)
	log.Printf("central: http=%s payment=%s gateway=%s",
		cfg.HTTPAddr, cfg.PaymentMode, cfg.GatewayInternalURL)
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
