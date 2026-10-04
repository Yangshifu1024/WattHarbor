package main

import (
	"context"
	"net/http"
	"time"

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
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

func newRouter(cfg config.Admin, r *resources, authAPI identityhttp.AdminAuth) *gin.Engine {
	db, orm, cache := r.DB, r.ORM, r.Cache
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

	return router
}
