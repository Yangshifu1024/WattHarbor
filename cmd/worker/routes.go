package main

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/internaljob"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/outbox"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/schedule"
	"github.com/gin-gonic/gin"
)

func newRouter(cfg config.Worker, r *resources, scheduler schedule.Scheduler) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	// 在注册路由前安装指标中间件，覆盖全部 HTTP 请求。
	// 指标端点无需运营会话，仅暴露路由模板、状态码与耗时。
	metrics := httpapi.NewMetrics("worker")
	router := httpapi.NewRouter(metrics)
	metrics.Register(router)
	router.GET("/health/live", func(c *gin.Context) { httpapi.OK(c, gin.H{"status": "live"}) })
	router.GET("/health/ready", func(c *gin.Context) {
		check, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if r.Stream.Ping(check).Err() != nil {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "stream unavailable", nil)
			return
		}
		for _, db := range []*sql.DB{r.GatewayDB.SQL, r.CentralDB.SQL, r.WorkerDB.SQL} {
			if db.PingContext(check) != nil {
				httpapi.Write(c, http.StatusServiceUnavailable, 5003, "database unavailable", nil)
				return
			}
		}
		httpapi.OK(c, gin.H{"status": "outbox_ready"})
	})

	internaljob.OpsAPI{Stream: r.Stream, Streams: outbox.DefaultBusinessStreams, ServiceToken: cfg.ServiceToken}.Register(router)
	schedule.API{Scheduler: scheduler, ServiceToken: cfg.ServiceToken}.Register(router)
	return router
}
