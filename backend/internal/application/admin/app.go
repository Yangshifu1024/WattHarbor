package admin

import (
	"context"
	"errors"
	"log"
	"net/http"

	exportpkg "wattharbor/internal/export"
	authhttp "wattharbor/internal/http/auth"
	gatewayclient "wattharbor/internal/integration/gateway"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/auth"
	"wattharbor/internal/platform/config"
	"wattharbor/internal/platform/serviceclient"
)

// Prepare 装配本模块的处理器和资源，内部请求使用单体提供的客户端。
func Prepare(ctx context.Context, internalHTTP *http.Client) (_ *Application, _ func() error, err error) {
	cfg, err := config.LoadAdmin()
	if err != nil {
		return nil, nil, err
	}
	resources, err := openResources(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	resources.InternalHTTP = internalHTTP
	app, err := assemble(ctx, cfg, resources)
	if err != nil {
		return nil, nil, errors.Join(err, resources.Close())
	}
	return app, resources.Close, nil
}

// Application 装配运营后台路由和导出任务。
type Application struct {
	Handler http.Handler
	Exports exportpkg.Cleaner // 导出过期文件清理，随应用启动和退出
}

// assemble 创建后台鉴权、网关客户端、路由和导出清理任务；可能初始化管理员账户。
// 这里只组装依赖，资源由单体入口释放。
func assemble(ctx context.Context, cfg config.Admin, r *resources) (*Application, error) {
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
	gateway, err := gatewayclient.NewClient(serviceclient.Client{HTTP: r.InternalHTTP}, config.GatewayServiceURL, cfg.ServiceToken)
	if err != nil {
		return nil, err
	}
	router, err := newRouter(cfg, r, authAPI, gateway)
	if err != nil {
		return nil, err
	}
	log.Printf("admin: export_dir=%s", cfg.ExportDir)
	return &Application{
		Handler: router,
		Exports: exportpkg.ExportService{DB: r.ORM, ExportDir: cfg.ExportDir},
	}, nil
}

// CleanupExports 运行后台导出清理，等待调用方取消。
func (a *Application) CleanupExports(ctx context.Context) error {
	return exportpkg.RunCleanup(ctx, a.Exports)
}
