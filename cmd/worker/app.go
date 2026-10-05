package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	centralclient "github.com/ChargePilot2026/charge-pilot/internal/integration/central"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/alerts"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/outbox"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/refund"
	workerruntime "github.com/ChargePilot2026/charge-pilot/internal/worker/runtime"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/schedule"
	"golang.org/x/sync/errgroup"
)

// run 在 ctx 下加载 worker 配置、打开资源并等待应用退出，返回启动、运行及关闭错误。
// 装配失败或所有受管理任务退出后才关闭资源，不启动脱离生命周期的任务。
func run(ctx context.Context) (err error) {
	cfg, err := config.LoadWorker()
	if err != nil {
		return err
	}
	resources, err := openResources(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resources.Close()) }()
	app, err := assemble(cfg, resources)
	if err != nil {
		return err
	}
	return app.Run(ctx)
}

// application 持有 worker 已装配的 HTTP 服务及受管理任务，不拥有独立资源关闭流程。
type application struct {
	Server *http.Server          // 受 httpapi.Serve 管理的监听服务。
	Jobs   workerruntime.Runtime // 同一生命周期下的调度、资金及同步任务。
}

// assemble 以 cfg 和已打开的 r 装配应用，返回应用或客户端配置错误；不执行业务请求或启动循环。
// 原 internalHTTP 用于查询及同步，jobHTTP 单独注入资金任务客户端，资源关闭仍由 run 管理。
func assemble(cfg config.Worker, r *resources) (*application, error) {
	internalHTTP, jobHTTP := r.InternalHTTP, r.JobHTTP
	central, err := centralclient.NewClient(serviceclient.Client{HTTP: internalHTTP}, cfg.CentralInternalURL, cfg.ServiceToken)
	if err != nil {
		return nil, err
	}
	gateway, err := gatewayclient.NewClient(serviceclient.Client{HTTP: internalHTTP}, cfg.GatewayInternalURL, cfg.ServiceToken)
	if err != nil {
		return nil, err
	}
	jobs, err := centralclient.NewClient(serviceclient.Client{HTTP: jobHTTP}, cfg.CentralInternalURL, cfg.ServiceToken)
	if err != nil {
		return nil, err
	}
	paidStarts := charge.PaidStarter{Orders: central, Gateway: gateway}
	startResults := charge.Synchronizer{Gateway: gateway, Central: central}
	endResults := charge.EndSynchronizer{Gateway: gateway, Central: central}
	cardEvents := charge.CardDispatcher{Gateway: gateway, Central: central}
	autoStops := charge.AutoStopper{Central: jobs}
	billingJobs := billing.Dispatcher{Central: jobs}
	refunds := refund.Dispatcher{Central: jobs}
	deviceAlerts := alerts.Sync{Central: central}
	scheduler := schedule.Scheduler{DB: r.WorkerDB.ORM, Handlers: map[string]schedule.Handler{}}

	publishers := []workerruntime.Publication{
		{Source: "gateway", Publisher: outbox.Publisher{Source: "gateway", DB: r.GatewayDB.ORM, Stream: r.Stream}},
		{Source: "user", Publisher: outbox.Publisher{Source: "user", DB: r.CentralDB.ORM, Stream: r.Stream}},
		{Source: "admin", Publisher: outbox.Publisher{Source: "admin", DB: r.CentralDB.ORM, Stream: r.Stream}},
	}
	router := newRouter(cfg, r, scheduler)
	log.Printf("worker: http=%s gateway=%s central=%s", cfg.HTTPAddr, cfg.GatewayInternalURL, cfg.CentralInternalURL)
	return &application{
		Server: &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second},
		Jobs: workerruntime.Runtime{Scheduler: scheduler, AutoStops: autoStops, Billing: billingJobs, Refunds: refunds,
			DeviceAlerts: deviceAlerts, CardEvents: cardEvents, PaidStarts: &paidStarts, StartResults: startResults, EndResults: endResults, Publishers: publishers},
	}, nil
}

// Run 在 ctx 下并发执行 HTTP、调度、资金和同步循环，任一错误取消其余任务并等待全部退出。
// 返回组内原错误；数据库、Redis 及客户端资源由外层在本方法退出后关闭。
func (a *application) Run(ctx context.Context) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return httpapi.Serve(groupCtx, a.Server, 10*time.Second) })
	group.Go(func() error { return a.Jobs.RunScheduler(groupCtx) })
	group.Go(func() error { return a.Jobs.RunFinance(groupCtx) })
	group.Go(func() error { return a.Jobs.RunSync(groupCtx) })
	return group.Wait()
}
