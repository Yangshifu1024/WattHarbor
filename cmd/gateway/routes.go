package main

import (
	"context"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/control"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/provision"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/repo"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/session"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// newRouter 接收 cfg、r、deviceConnections、orders、compensation 与 userStops，注册原接口、指标及健康检查并返回 router；不启动监听，健康处理器按请求 context 检查数据库。
func newRouter(cfg config.Gateway, r *resources, deviceConnections *session.Registry, orders control.CentralAuthorizer, compensation control.Compensation, userStops control.UserStopService) *gin.Engine {
	db, orm := r.DB, r.ORM
	sink := repo.MySQLSink{DB: orm}
	gin.SetMode(gin.ReleaseMode)
	// 在注册路由前安装指标中间件，覆盖全部 HTTP 请求。
	// 指标端点无需运营会话，仅暴露路由模板、状态码与耗时。
	metrics := httpapi.NewMetrics("gateway")
	router := httpapi.NewRouter(metrics)
	metrics.Register(router)
	provision.API{DB: orm, ServiceToken: cfg.ServiceToken}.Register(router)
	control.CardReplyAPI{Devices: deviceConnections, ServiceToken: cfg.ServiceToken}.Register(router)
	control.StartAPI{Service: control.StartService{
		Orders: orders,
		Repo:   sink, Devices: deviceConnections,
	}, ServiceToken: cfg.ServiceToken}.Register(router)
	control.CompensationAPI{Service: compensation, ServiceToken: cfg.ServiceToken}.Register(router)
	control.UserStopAPI{Service: userStops, ServiceToken: cfg.ServiceToken}.Register(router)
	control.ScanAPI{Scanner: sink, ServiceToken: cfg.ServiceToken}.Register(router)
	control.DeviceSummaryAPI{DB: orm, ServiceToken: cfg.ServiceToken}.Register(router)
	control.TelemetryAPI{DB: orm, ServiceToken: cfg.ServiceToken, MaxWindow: 24 * time.Hour, MaxPoints: 2000}.Register(router)
	control.SessionAPI{DB: orm, ServiceToken: cfg.ServiceToken, IdleThreshold: 15 * time.Minute, MaxRows: 500}.Register(router)
	router.GET("/health/live", func(c *gin.Context) { httpapi.OK(c, gin.H{"status": "live"}) })
	router.GET("/health/ready", func(c *gin.Context) {
		checkCtx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(checkCtx); err != nil {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "database unavailable", nil)
			return
		}
		httpapi.OK(c, gin.H{"status": "device_ingress_ready"})
	})

	return router
}
