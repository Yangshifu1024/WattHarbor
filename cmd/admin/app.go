package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	exportpkg "github.com/ChargePilot2026/charge-pilot/internal/export"
	sessionstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/redis"
	identityhttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	adminexport "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/export"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"golang.org/x/sync/errgroup"
)

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

type application struct {
	Server  *http.Server
	Exports exportpkg.Cleaner
}

func assemble(ctx context.Context, cfg config.Admin, r *resources) (*application, error) {
	jwt, err := auth.NewJWT(cfg.JWTSecret)
	if err != nil {
		return nil, err
	}
	adminStore := ops.AdminStore{DB: r.ORM}
	if err := adminStore.Bootstrap(ctx, cfg.AdminBootstrapUser, cfg.AdminBootstrapPassword); err != nil {
		return nil, err
	}
	authAPI := identityhttp.AdminAuth{Store: adminStore, Sessions: sessionstore.AdminSessions{Redis: r.Cache}, JWT: jwt}
	router := newRouter(cfg, r, authAPI)
	log.Printf("admin: http=%s gateway=%s export_dir=%s", cfg.HTTPAddr, cfg.GatewayInternalURL, cfg.ExportDir)
	return &application{Server: &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second}, Exports: adminexport.ExportTask{DB: r.ORM, ExportDir: cfg.ExportDir}}, nil
}
func (a *application) Run(ctx context.Context) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return httpapi.Serve(groupCtx, a.Server, 10*time.Second) })
	group.Go(func() error { return exportpkg.RunCleanup(groupCtx, a.Exports) })
	return group.Wait()
}
