package worker

import (
	"context"
	"errors"
	"net/http"

	adminops "wattharbor/internal/http/admin/ops"
	centralclient "wattharbor/internal/integration/central"
	gatewayclient "wattharbor/internal/integration/gateway"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/config"
	"wattharbor/internal/platform/diagnostics"
	"wattharbor/internal/platform/serviceclient"
	"wattharbor/internal/worker/alerts"
	"wattharbor/internal/worker/billing"
	"wattharbor/internal/worker/charge"
	"wattharbor/internal/worker/outbox"
	"wattharbor/internal/worker/refund"
	workerruntime "wattharbor/internal/worker/runtime"
	"wattharbor/internal/worker/schedule"
)

// Prepare 装配本模块的处理器和资源，内部请求使用单体提供的客户端。
func Prepare(ctx context.Context, internalHTTP *http.Client) (_ *Application, _ func() error, err error) {
	cfg, err := config.LoadWorker()
	if err != nil {
		return nil, nil, err
	}
	resources, err := openResources(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	resources.InternalHTTP = internalHTTP
	app, err := assemble(cfg, resources)
	if err != nil {
		return nil, nil, errors.Join(err, resources.Close())
	}
	return app, resources.Close, nil
}

// Application 装配后台任务及内部路由。
type Application struct {
	Handler http.Handler
	Jobs    workerruntime.Runtime // 同一生命周期下的调度、资金及同步任务。
}

// assemble 创建内部客户端、调度器和三个事件发布器，不启动任务。
// 查询和资金任务共用进程内客户端；调度处理器表目前为空。
func assemble(cfg config.Worker, r *resources) (*Application, error) {
	central, err := centralclient.NewClient(serviceclient.Client{HTTP: r.InternalHTTP}, config.CentralServiceURL, cfg.ServiceToken)
	if err != nil {
		return nil, err
	}
	gateway, err := gatewayclient.NewClient(serviceclient.Client{HTTP: r.InternalHTTP}, config.GatewayServiceURL, cfg.ServiceToken)
	if err != nil {
		return nil, err
	}
	paidStarts := charge.PaidStarter{Orders: central, Gateway: gateway}
	startResults := charge.Synchronizer{Gateway: gateway, Central: central}
	endResults := charge.EndSynchronizer{Gateway: gateway, Central: central}
	cardEvents := charge.CardDispatcher{Gateway: gateway, Central: central}
	autoStops := charge.AutoStopper{Central: central}
	billingJobs := billing.Dispatcher{Central: central}
	refunds := refund.Dispatcher{Central: central}
	deviceAlerts := alerts.Sync{Central: central}
	scheduler := schedule.Scheduler{DB: r.WorkerDB.ORM, Handlers: map[string]schedule.Handler{}}

	publishers := []workerruntime.Publication{
		{Source: "gateway", Publisher: outbox.Publisher{Source: "gateway", UnifiedGateway: cfg.UnifiedDatabase, DB: r.GatewayDB.ORM, Stream: r.Stream}},
		{Source: "user", Publisher: outbox.Publisher{Source: "user", DB: r.CentralDB.ORM, Stream: r.Stream}},
		{Source: "admin", Publisher: outbox.Publisher{Source: "admin", DB: r.CentralDB.ORM, Stream: r.Stream}},
	}
	tracker := &diagnostics.Registry{}
	router := newRouter(cfg, r, scheduler)
	adminops.RegisterInternalDiagnostics(router, cfg.ServiceToken, func(context.Context) ops.DiagnosticsReply {
		return ops.WorkerDiagnostics(tracker)
	})
	return &Application{
		Handler: router,
		Jobs: workerruntime.Runtime{
			Scheduler:    scheduler,
			Diagnostics:  tracker,
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
