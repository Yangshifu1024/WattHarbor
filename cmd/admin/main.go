// 命令 admin 是运营后台进程：只暴露 /api/v1/admin/* 与后台自己的健康检查。
//
// 它与 central 共用 central_db，但只取后台真正需要的依赖：会话在 cache、
// 令牌用同一份 JWT_SECRET、厂商等设备能力经网关读取。微信与支付配置一概不取——
// 后台不替用户登录，也不发起支付。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	sessionstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/redis"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	identityhttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	admindashboard "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/dashboard"
	admindevice "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/device"
	adminexport "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/export"
	adminfinance "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/finance"
	adminmarketing "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/marketing"
	adminops "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/ops"
	adminorder "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/order"
	adminpricing "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/pricing"
	adminstation "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/station"
	adminuser "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/user"
	adminvendor "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/vendor"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadAdmin()
	if err != nil {
		return err
	}
	db, err := dbconn.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		return err
	}
	redisOptions, err := redis.ParseURL(cfg.RedisCacheURL)
	if err != nil {
		return err
	}
	cache := redis.NewClient(redisOptions)
	defer cache.Close()
	if err := cache.Ping(ctx).Err(); err != nil {
		return err
	}
	jwt, err := auth.NewJWT(cfg.JWTSecret)
	if err != nil {
		return err
	}
	gin.SetMode(gin.ReleaseMode)
	// 指标端点无需运营会话，只暴露路由模板、状态码与耗时。
	metrics := httpapi.NewMetrics("admin")
	router := httpapi.NewRouter(metrics)
	metrics.Register(router)
	router.GET("/health/live", func(c *gin.Context) { httpapi.OK(c, gin.H{"status": "live"}) })
	router.GET("/health/ready", func(c *gin.Context) {
		check, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if db.PingContext(check) != nil || cache.Ping(check).Err() != nil {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "storage unavailable", nil)
			return
		}
		httpapi.OK(c, gin.H{"status": "admin_storage_ready"})
	})

	adminStore := ops.AdminStore{DB: orm}
	if err := adminStore.Bootstrap(ctx, cfg.AdminBootstrapUser, cfg.AdminBootstrapPassword); err != nil {
		return err
	}
	authAPI := identityhttp.AdminAuth{Store: adminStore, Sessions: sessionstore.AdminSessions{Redis: cache}, JWT: jwt}
	authAPI.Register(router)
	// 后台业务按族注册：每个族是一个独立包，进程只挂自己提供的那些。
	adminops.API{DB: orm, Auth: authAPI}.Register(router)
	adminpricing.API{DB: orm, Auth: authAPI}.Register(router)
	adminstation.API{DB: orm, Auth: authAPI}.Register(router)
	admindevice.API{DB: orm, Auth: authAPI, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}.Register(router)
	adminorder.API{DB: orm, Auth: authAPI, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}.Register(router)
	adminfinance.API{DB: orm, Auth: authAPI}.Register(router)
	adminmarketing.API{DB: orm, Auth: authAPI}.Register(router)
	adminuser.API{DB: orm, Auth: authAPI}.Register(router)
	adminvendor.API{DB: orm, Auth: authAPI, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}.Register(router)
	admindashboard.Dashboard{DB: orm}.Register(router, authAPI)
	chargehttp.AdminAccounts{DB: orm, Auth: &authAPI}.RegisterRoles(router)
	chargehttp.AdminAccounts{DB: orm, Auth: &authAPI}.Register(router)
	adminexport.ExportTask{DB: orm, Auth: authAPI, ExportDir: cfg.ExportDir}.Register(router)

	// 导出文件有过期时间，过期件定时清理，避免目录无限增长。
	cleanup := adminexport.ExportTask{DB: orm, ExportDir: cfg.ExportDir}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if count, err := cleanup.CleanupExpired(ctx); err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("export cleanup: %v", err)
				}
			} else if count > 0 {
				log.Printf("expired %d export files", count)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	log.Printf("admin: http=%s gateway=%s export_dir=%s",
		cfg.HTTPAddr, cfg.GatewayInternalURL, cfg.ExportDir)
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServe(); errors.Is(err, http.ErrServerClosed) {
		return nil
	} else {
		return err
	}
}
