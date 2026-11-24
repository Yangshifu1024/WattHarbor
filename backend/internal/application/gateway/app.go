package gateway

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"wattharbor/internal/gateway/control"
	"wattharbor/internal/gateway/repo"
	gatewayruntime "wattharbor/internal/gateway/runtime"
	"wattharbor/internal/gateway/session"
	adminops "wattharbor/internal/http/admin/ops"
	centralclient "wattharbor/internal/integration/central"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/config"
	"wattharbor/internal/platform/diagnostics"
	"wattharbor/internal/platform/serviceclient"
	"wattharbor/internal/protocol"
	"wattharbor/internal/protocol/dc589"
)

// Prepare 装配本模块的处理器和资源，内部请求使用单体提供的客户端。
func Prepare(ctx context.Context, internalHTTP *http.Client, notify func()) (_ *Application, _ func() error, err error) {
	cfg, err := config.LoadGateway()
	if err != nil {
		return nil, nil, err
	}
	resources, err := openResources(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	resources.InternalHTTP = internalHTTP
	resources.Notify = notify
	app, err := assemble(cfg, resources)
	if err != nil {
		return nil, nil, errors.Join(err, resources.Close())
	}
	return app, resources.Close, nil
}

// Application 装配网关路由和设备任务。
type Application struct {
	Handler http.Handler
	// Devices 管理设备监听、事件记录、停止重试与投递历史清理。
	Devices gatewayruntime.Runtime
}

// assemble 创建中央客户端、设备连接表、事件记录器和路由，不启动监听或后台循环。
func assemble(cfg config.Gateway, r *resources) (*Application, error) {
	central, err := centralclient.NewClient(serviceclient.Client{HTTP: r.InternalHTTP}, config.CentralServiceURL, cfg.ServiceToken)
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
	log.Printf("gateway: dc589=%s max_connections=%d idle_heartbeat_seconds=%d charging_heartbeat_seconds=%d debug_heartbeat=%t timezone=%s offset_seconds=%d", cfg.DC589Addr, cfg.MaxConnections, dc589.DefaultHeartbeatSeconds, dc589.ChargingHeartbeatSeconds, cfg.DebugHeartbeat, zone, offset)
	return &Application{
		Handler: router,
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
