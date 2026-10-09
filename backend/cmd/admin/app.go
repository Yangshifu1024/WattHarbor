package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	exportpkg "chargepilot/internal/export"
	authhttp "chargepilot/internal/http/auth"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/ops"
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/config"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"golang.org/x/sync/errgroup"
)

// run 加载配置并启动应用，等待请求和任务退出后释放资源；返回启动、运行和清理错误。
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

// application 管理 HTTP 服务和导出清理任务的启动与退出。
type application struct {
	Server  *http.Server      // 由 httpapi.Serve 处理停机及等待当前请求
	Exports exportpkg.Cleaner // 导出过期文件清理，随应用启动和退出
}

// assemble 创建后台鉴权、网关客户端、路由和导出清理任务；可能初始化管理员账户。
// 这里只组装依赖，应用由 Run 启动，资源由 run 释放。
func assemble(ctx context.Context, cfg config.Admin, r *resources) (*application, error) {
	jwt, err := auth.NewJWT(cfg.JWTSecret)
	if err != nil {
		return nil, err
	}
	adminStore := ops.AccountRepo{DB: r.ORM}
	if err := adminStore.Bootstrap(ctx, cfg.AdminBootstrapUser, cfg.AdminBootstrapPassword); err != nil {
		return nil, err
	}
	authAPI := authhttp.AdminAuth{
		Sessions: ops.SessionService{
			Accounts: adminStore,
			Sessions: ops.SessionRepo{Redis: r.Cache},
			Tokens:   ops.JWTAccess{JWT: jwt},
			Limits:   ops.AuthRateRepo{Redis: r.Cache},
		},
		MFA: ops.MFAService{DB: adminStore.DB, Repo: ops.MFARepo{Redis: r.Cache}},
	}
	gateway, err := gatewayclient.NewClient(serviceclient.Client{HTTP: r.InternalHTTP}, cfg.GatewayInternalURL, cfg.ServiceToken)
	if err != nil {
		return nil, err
	}
	router, err := newRouter(cfg, r, authAPI, gateway)
	if err != nil {
		return nil, err
	}
	log.Printf("admin: http=%s gateway=%s export_dir=%s", cfg.HTTPAddr, cfg.GatewayInternalURL, cfg.ExportDir)
	return &application{
		Server: &http.Server{
			Addr:              cfg.HTTPAddr,
			Handler:           router,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       90 * time.Second,
		},
		Exports: exportpkg.ExportService{DB: r.ORM, ExportDir: cfg.ExportDir},
	}, nil
}

// Run 同时运行 HTTP 服务和导出清理；取消或出错后等待两者退出。
func (a *application) Run(ctx context.Context) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return httpapi.Serve(groupCtx, a.Server, 10*time.Second) })
	group.Go(func() error { return exportpkg.RunCleanup(groupCtx, a.Exports) })
	return group.Wait()
}
