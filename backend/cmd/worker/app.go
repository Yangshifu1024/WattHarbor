package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	centralclient "chargepilot/internal/integration/central"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/platform/config"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/worker/alerts"
	"chargepilot/internal/worker/billing"
	"chargepilot/internal/worker/charge"
	"chargepilot/internal/worker/outbox"
	"chargepilot/internal/worker/refund"
	workerruntime "chargepilot/internal/worker/runtime"
	"chargepilot/internal/worker/schedule"
	"golang.org/x/sync/errgroup"
)

// run 加载配置并启动应用，等待请求和任务退出后释放资源；返回启动、运行和清理错误。
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

// application 管理 HTTP 服务和后台任务，资源由 run 释放。
type application struct {
	Server *http.Server          // 受 httpapi.Serve 管理的监听服务。
	Jobs   workerruntime.Runtime // 同一生命周期下的调度、资金及同步任务。
}

// assemble 创建内部客户端、调度器和三个事件发布器，不启动任务。
// 查询使用 InternalHTTP，资金任务使用 JobHTTP；调度处理器表目前为空。
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
		Server: &http.Server{
			Addr:              cfg.HTTPAddr,
			Handler:           router,
			ReadHeaderTimeout: 5 * time.Second,
		},
		Jobs: workerruntime.Runtime{
			Scheduler:    scheduler,
			AutoStops:    autoStops,
			Billing:      billingJobs,
			Refunds:      refunds,
			DeviceAlerts: deviceAlerts,
			CardEvents:   cardEvents,
			PaidStarts:   &paidStarts,
			StartResults: startResults,
			EndResults:   endResults,
			Publishers:   publishers,
		},
	}, nil
}

// Run 同时运行 HTTP、调度、资金和同步任务；任一出错时取消其他任务并等待全部退出。
func (a *application) Run(ctx context.Context) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return httpapi.Serve(groupCtx, a.Server, 10*time.Second) })
	group.Go(func() error { return a.Jobs.RunScheduler(groupCtx) })
	group.Go(func() error { return a.Jobs.RunFinance(groupCtx) })
	group.Go(func() error { return a.Jobs.RunSync(groupCtx) })
	return group.Wait()
}
