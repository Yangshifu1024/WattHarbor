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

type application struct {
	Server *http.Server
	Jobs   workerruntime.Runtime
}

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
	paidStarts := charge.PaidStarter{Orders: central, Gateway: gateway}
	startResults := charge.Synchronizer{Gateway: gateway, Central: central}
	endResults := charge.EndSynchronizer{Gateway: gateway, Central: central}
	cardEvents := charge.CardDispatcher{Gateway: gateway, Central: central}
	autoStops := charge.AutoStopper{CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken, Client: serviceclient.Client{HTTP: jobHTTP}}
	billingJobs := billing.Dispatcher{CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken, Client: jobHTTP}
	refunds := refund.Dispatcher{CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken, Client: jobHTTP}
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
func (a *application) Run(ctx context.Context) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return httpapi.Serve(groupCtx, a.Server, 10*time.Second) })
	group.Go(func() error { return a.Jobs.RunScheduler(groupCtx) })
	group.Go(func() error { return a.Jobs.RunFinance(groupCtx) })
	group.Go(func() error { return a.Jobs.RunSync(groupCtx) })
	return group.Wait()
}
