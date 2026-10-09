package main

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
	centralclient "chargepilot/internal/integration/central"
	"chargepilot/internal/platform/config"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/protocol"
	"chargepilot/internal/protocol/dc589"
	"golang.org/x/sync/errgroup"
)

// run 接收进程 ctx，加载配置、打开并装配资源、等待应用退出后关闭资源；返回启动、运行与清理的合并错误。
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

// application 持有已装配的 HTTP 服务与设备运行编排，负责受管理启动和等待退出，资源关闭仍由 run 执行。
type application struct {
	// Server 原受管理 HTTP 服务器。
	Server *http.Server
	// Devices 已装配的设备运行编排，持有原监听、事件 sink 及停止恢复能力。
	Devices gatewayruntime.Runtime
}

// assemble 接收 cfg 与已打开 r，注入中央客户端、同一命令 repo 和连接注册表，返回应用或配置错误；仅装配，不启动监听或循环。
// cfg 提供中央地址、服务令牌、HTTP／设备监听地址、连接上限与协议调试开关；r 提供同池 SQL／ORM 与五秒内部 HTTP 客户端。
// 成功返回未启动的应用与 nil，中央客户端配置失败返回 nil 与原错误；创建注册表、sink 与路由并记录启动配置，不写设备状态。
func assemble(cfg config.Gateway, r *resources) (*application, error) {
	central, err := centralclient.NewClient(serviceclient.Client{HTTP: r.InternalHTTP}, cfg.CentralInternalURL, cfg.ServiceToken)
	if err != nil {
		return nil, err
	}
	connections := &session.Registry{}
	sink := repo.MySQLSink{DB: r.ORM}
	orders := control.CentralAuthorizer{Central: central}
	compensation := control.Compensation{Repo: sink, Devices: connections}
	userStops := control.UserStopService{Orders: orders, Repo: sink, Devices: connections}
	router := newRouter(cfg, r, connections, orders, compensation, userStops)
	zone, offset := time.Now().Zone()
	log.Printf("gateway starting http=%s dc589=%s max_connections=%d idle_heartbeat_seconds=%d charging_heartbeat_seconds=%d debug_heartbeat=%t timezone=%s offset_seconds=%d", cfg.HTTPAddr, cfg.DC589Addr, cfg.MaxConnections, dc589.DefaultHeartbeatSeconds, dc589.ChargingHeartbeatSeconds, cfg.DebugHeartbeat, zone, offset)
	return &application{
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
			Sink:           sink,
			MaxConnections: cfg.MaxConnections,
			Compensation:   compensation,
			UserStops:      userStops,
		},
	}, nil
}

// Run 接收 ctx，受管理地启动 HTTP、设备服务与停止恢复，任一失败取消同组并等待全部退出；返回运行错误，不关闭装配方资源。
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
