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

// newRouter 注册当前进程路由、指标与健康端点；cfg 提供服务令牌，r 提供三库与事件流 Redis，scheduler 提供调度接口。
// 返回未监听的 Gin 引擎，无错误返回；设置全局 Gin 发布模式，仅装配不执行业务请求，健康处理器用请求 context 的两秒子期限检查存储。
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

	internaljob.OpsAPI{
		Stream:       r.Stream,
		Streams:      outbox.DefaultBusinessStreams,
		ServiceToken: cfg.ServiceToken,
	}.Register(router)
	schedule.API{Scheduler: scheduler, ServiceToken: cfg.ServiceToken}.Register(router)
	return router
}
