package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"chargepilot/internal/gateway/control"
	"chargepilot/internal/gateway/repo"
	gatewayruntime "chargepilot/internal/gateway/runtime"
	"chargepilot/internal/gateway/session"
	adminops "chargepilot/internal/http/admin/ops"
	centralclient "chargepilot/internal/integration/central"
	"chargepilot/internal/ops"
	"chargepilot/internal/platform/config"
	"chargepilot/internal/platform/diagnostics"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/protocol"
	"chargepilot/internal/protocol/dc589"
	"golang.org/x/sync/errgroup"
)

// Run 启动兼容的独立进程。
func Run(ctx context.Context) (err error) {
	app, close, err := Prepare(ctx, nil, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, close()) }()
	return app.Run(ctx)
}

// Prepare 装配应用和资源；非空 internalHTTP 用于同进程内部调用。
func Prepare(ctx context.Context, internalHTTP *http.Client, notify func()) (_ *Application, _ func() error, err error) {
	cfg, err := config.LoadGateway()
	if err != nil {
		return nil, nil, err
	}
	if internalHTTP != nil {
		cfg.CentralInternalURL = "http://central"
	}
	resources, err := openResources(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	if internalHTTP != nil {
		resources.InternalHTTP.CloseIdleConnections()
		resources.InternalHTTP = internalHTTP

	}
	resources.Notify = notify
	app, err := assemble(cfg, resources)
	if err != nil {
		return nil, nil, errors.Join(err, resources.Close())
	}
	return app, resources.Close, nil
}

// application 管理HTTP、设备接入、停止重试与投递历史清理，资源由run在所有任务退出后释放。
type Application struct {
	// Server 是随应用启动和退出的 HTTP 服务。
	Server *http.Server
	// Devices 管理设备监听、事件记录、停止重试与投递历史清理。
	Devices gatewayruntime.Runtime
}

// assemble 创建中央客户端、设备连接表、事件记录器和路由，不启动监听或后台循环。
func assemble(cfg config.Gateway, r *resources) (*Application, error) {
	central, err := centralclient.NewClient(serviceclient.Client{HTTP: r.InternalHTTP}, cfg.CentralInternalURL, cfg.ServiceToken)
	if err != nil {
		return nil, err
	}
	connections := &session.Registry{}
	snapshots := &repo.ProcessSnapshots{}
	sink := repo.MySQLSink{DB: r.ORM, Snapshots: snapshots, NotifyDelivery: r.Notify, UnifiedOutbox: cfg.UnifiedDatabase}
	orders := control.CentralAuthorizer{Central: central}
	compensation := control.Compensation{Repo: sink, Devices: connections}
	userStops := control.UserStopService{Orders: orders, Repo: sink, Devices: connections}
	tracker := &diagnostics.Registry{}
	router := newRouter(cfg, r, connections, orders, compensation, userStops, snapshots)
	adminops.RegisterInternalDiagnostics(router, cfg.ServiceToken, func(ctx context.Context) ops.DiagnosticsReply {
		return ops.GatewayDiagnostics(ctx, r.ORM, tracker)
	})
	zone, offset := time.Now().Zone()
	log.Printf("gateway starting http=%s dc589=%s max_connections=%d idle_heartbeat_seconds=%d charging_heartbeat_seconds=%d debug_heartbeat=%t timezone=%s offset_seconds=%d", cfg.HTTPAddr, cfg.DC589Addr, cfg.MaxConnections, dc589.DefaultHeartbeatSeconds, dc589.ChargingHeartbeatSeconds, cfg.DebugHeartbeat, zone, offset)
	return &Application{
		Server: &http.Server{
			Addr:              cfg.HTTPAddr,
			Handler:           router,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       90 * time.Second,
		},
		Devices: gatewayruntime.Runtime{
			Endpoints: []protocol.Endpoint{{
				Address: cfg.DC589Addr,
				Adapter: dc589.TCPAdapter{Registry: connections, DebugHeartbeat: cfg.DebugHeartbeat},
			}},
			Sink:           session.SerializedSink{Registry: connections, Sink: sink},
			MaxConnections: cfg.MaxConnections,
			Diagnostics:    tracker,
			Compensation:   compensation,
			UserStops:      userStops,
			SyncHistory:    sink,
		},
	}, nil
}

// Run 同时运行HTTP、设备接入、停止重试与历史清理；任一出错时取消其他任务并等待全部退出。
func (a *Application) Run(ctx context.Context) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return httpapi.Serve(groupCtx, a.Server, 10*time.Second) })
	group.Go(func() error { return a.Devices.Serve(groupCtx) })
	group.Go(func() error { return a.Devices.RetryStops(groupCtx) })
	group.Go(func() error { return a.Devices.CleanupSyncHistory(groupCtx) })
	if err := group.Wait(); err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	return nil
}
