package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"wattharbor/internal/gateway/control"
	"wattharbor/internal/gateway/provision"
	"wattharbor/internal/gateway/repo"
	"wattharbor/internal/gateway/session"
	"wattharbor/internal/platform/config"
	"wattharbor/internal/platform/httpapi"
)

// newRouter 注册本进程的业务路由、指标和健康检查，不启动监听。
// 健康检查使用请求 context，最多等待两秒。
func newRouter(cfg config.Gateway, r *resources, deviceConnections *session.Registry, orders control.CentralAuthorizer, compensation control.Compensation, userStops control.UserStopService, snapshots *repo.ProcessSnapshots) *gin.Engine {
	db, orm := r.DB, r.ORM
	sink := repo.MySQLSink{DB: orm}
	gin.SetMode(gin.ReleaseMode)
	// 在注册路由前安装指标中间件，覆盖全部 HTTP 请求。
	// /metrics 无需登录，包含请求、进程和数据库连接池统计。
	metrics := httpapi.NewMetrics("gateway", httpapi.DatabasePool{Schema: "gateway_db", DB: r.DB})
	router := httpapi.NewRouter(metrics)
	metrics.Register(router)
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

	// 内部端点统一使用同一服务令牌：内部调用只认 X-Service-Token，不暴露到前端。
	token := cfg.ServiceToken
	provision.API{DB: orm, ServiceToken: token}.Register(router)
	control.CardReplyAPI{Devices: deviceConnections, ServiceToken: token}.Register(router)
	control.StartAPI{
		Service: control.StartService{
			Orders:  orders,
			Repo:    sink,
			Devices: deviceConnections,
		},
		ServiceToken: token,
	}.Register(router)
	control.CompensationAPI{Service: compensation, ServiceToken: token}.Register(router)
	control.UserStopAPI{Service: userStops, ServiceToken: token}.Register(router)
	control.ScanAPI{Scanner: sink, ServiceToken: token}.Register(router)
	control.DeviceSummaryAPI{DB: orm, ServiceToken: token}.Register(router)
	// 注册设备事件、计量证据和订单采样的内部接口。
	control.DeviceEventAPI{
		Snapshots:    snapshots,
		DB:           orm,
		Devices:      deviceConnections,
		ServiceToken: token,
	}.Register(router)
	control.SessionAPI{
		DB:            orm,
		IdleThreshold: 15 * time.Minute,
		MaxRows:       500,
		ServiceToken:  token,
	}.Register(router)

	return router
}
