package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	exportpkg "github.com/ChargePilot2026/charge-pilot/internal/export"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	identityhttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	adminexport "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/export"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"golang.org/x/sync/errgroup"
)

// run 在 ctx 下加载配置、打开资源并运行后台进程，返回初始化、执行及释放错误；退出前等待受管理任务。
func run(ctx context.Context) (err error) {
	cfg, err := config.LoadAdmin()
	if err != nil {
		return err
	}
	resources, err := openResources(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resources.Close()) }()
	app, err := assemble(ctx, cfg, resources)
	if err != nil {
		return err
	}
	return app.Run(ctx)
}

// application 管理后台 HTTP 及导出清理的共同生命周期。
type application struct {
	Server  *http.Server      // 由 httpapi.Serve 处理停机及等待当前请求
	Exports exportpkg.Cleaner // 原导出过期文件清理能力，不拥有独立未管理循环
}

// assemble 在 ctx 下按 cfg 及 r 装配鉴权、内部客户端、路由和清理，返回应用或装配错误。
// bootstrap 沿用原账号初始化规则，客户端不启动后台任务，资源释放由 run 管理。
func assemble(ctx context.Context, cfg config.Admin, r *resources) (*application, error) {
	jwt, err := auth.NewJWT(cfg.JWTSecret)
	if err != nil {
		return nil, err
	}
	adminStore := ops.AdminStore{DB: r.ORM}
	if err := adminStore.Bootstrap(ctx, cfg.AdminBootstrapUser, cfg.AdminBootstrapPassword); err != nil {
		return nil, err
	}
	authAPI := identityhttp.AdminAuth{
		Sessions: ops.SessionService{Accounts: adminStore, Sessions: ops.SessionRepo{Redis: r.Cache}, Tokens: ops.JWTAccess{JWT: jwt}, Limits: ops.AuthRateRepo{Redis: r.Cache}},
		MFA:      ops.MFAService{DB: adminStore.DB, Repo: ops.MFARepo{Redis: r.Cache}},
	}
	gateway, err := gatewayclient.NewClient(serviceclient.Client{HTTP: r.InternalHTTP}, cfg.GatewayInternalURL, cfg.ServiceToken)
	if err != nil {
		return nil, err
	}
	router := newRouter(cfg, r, authAPI, gateway)
	log.Printf("admin: http=%s gateway=%s export_dir=%s", cfg.HTTPAddr, cfg.GatewayInternalURL, cfg.ExportDir)
	return &application{Server: &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second}, Exports: adminexport.ExportTask{DB: r.ORM, ExportDir: cfg.ExportDir}}, nil
}

// Run 在 ctx 下管理 a 的 HTTP 与清理循环，返回首个执行错误；取消后等待两者退出才允许释放资源。
func (a *application) Run(ctx context.Context) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return httpapi.Serve(groupCtx, a.Server, 10*time.Second) })
	group.Go(func() error { return exportpkg.RunCleanup(groupCtx, a.Exports) })
	return group.Wait()
}
