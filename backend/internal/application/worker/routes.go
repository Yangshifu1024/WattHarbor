package worker

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"chargepilot/internal/platform/config"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/worker/internaljob"
	"chargepilot/internal/worker/outbox"
	"chargepilot/internal/worker/schedule"
	"github.com/gin-gonic/gin"
)

// newRouter 注册本进程的业务路由、指标和健康检查，不启动监听。
// 健康检查使用请求 context，最多等待两秒。
func newRouter(cfg config.Worker, r *resources, scheduler schedule.Scheduler) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	// 在注册路由前安装指标中间件，覆盖全部 HTTP 请求。
	// /metrics 无需登录，包含请求、进程和数据库连接池统计。
	metrics := httpapi.NewMetrics("worker", httpapi.DatabasePool{Schema: "gateway_db", DB: r.GatewayDB.SQL}, httpapi.DatabasePool{Schema: "central_db", DB: r.CentralDB.SQL}, httpapi.DatabasePool{Schema: "worker_db", DB: r.WorkerDB.SQL})
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
