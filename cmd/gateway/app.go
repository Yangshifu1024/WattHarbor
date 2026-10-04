package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/control"
	gatewayruntime "github.com/ChargePilot2026/charge-pilot/internal/gateway/runtime"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/session"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	centralclient "github.com/ChargePilot2026/charge-pilot/internal/integration/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol/dc589"
	"golang.org/x/sync/errgroup"
)

func run(ctx context.Context) (err error) {
	cfg, err := config.LoadGateway()
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
	Server  *http.Server
	Devices gatewayruntime.Runtime
}

func assemble(cfg config.Gateway, r *resources) (*application, error) {
	central, err := centralclient.NewClient(serviceclient.Client{HTTP: r.InternalHTTP}, cfg.CentralInternalURL, cfg.ServiceToken)
	if err != nil {
		return nil, err
	}
	connections := &session.Registry{}
	sink := store.MySQLSink{DB: r.ORM}
	orders := control.CentralAuthorizer{Central: central}
	compensation := control.Compensation{Store: sink, Devices: connections}
	userStops := control.UserStopService{Orders: orders, Store: sink, Devices: connections}
	router := newRouter(cfg, r, connections, orders, compensation, userStops)
	zone, offset := time.Now().Zone()
	log.Printf("gateway starting http=%s dc589=%s max_connections=%d idle_heartbeat_seconds=%d charging_heartbeat_seconds=%d debug_heartbeat=%t timezone=%s offset_seconds=%d", cfg.HTTPAddr, cfg.DC589Addr, cfg.MaxConnections, dc589.DefaultHeartbeatSeconds, dc589.ChargingHeartbeatSeconds, cfg.DebugHeartbeat, zone, offset)
	return &application{
		Server:  &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second},
		Devices: gatewayruntime.Runtime{Endpoints: []protocol.Endpoint{{Address: cfg.DC589Addr, Adapter: dc589.TCPAdapter{Registry: connections, DebugHeartbeat: cfg.DebugHeartbeat}}}, Sink: sink, MaxConnections: cfg.MaxConnections, Compensation: compensation, UserStops: userStops},
	}, nil
}
func (a *application) Run(ctx context.Context) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return httpapi.Serve(groupCtx, a.Server, 10*time.Second) })
	group.Go(func() error { return a.Devices.Serve(groupCtx) })
	group.Go(func() error { return a.Devices.RetryStops(groupCtx) })
	if err := group.Wait(); err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	return nil
}
