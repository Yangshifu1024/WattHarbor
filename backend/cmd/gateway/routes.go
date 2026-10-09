package main

import (
	"context"
	"net/http"
	"time"

	"chargepilot/internal/gateway/control"
	"chargepilot/internal/gateway/provision"
	"chargepilot/internal/gateway/repo"
	"chargepilot/internal/gateway/session"
	"chargepilot/internal/platform/config"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// newRouter 接收 cfg、r、deviceConnections、orders、compensation 与 userStops，注册原接口、指标及健康检查并返回 router；不启动监听，健康处理器按请求 context 检查数据库。
// cfg 提供服务令牌，r 提供 SQL／ORM；deviceConnections 持有设备连接，orders 提供中央授权，compensation 与 userStops 提供既有停止恢复。
// 返回未监听的 Gin 引擎，无错误返回；设置全局发布模式，注册时不查询业务存储或发送设备指令。
func newRouter(cfg config.Gateway, r *resources, deviceConnections *session.Registry, orders control.CentralAuthorizer, compensation control.Compensation, userStops control.UserStopService) *gin.Engine {
	db, orm := r.DB, r.ORM
	sink := repo.MySQLSink{DB: orm}
	gin.SetMode(gin.ReleaseMode)
	// 在注册路由前安装指标中间件，覆盖全部 HTTP 请求。
	// 指标与健康端点先于业务路由注册，与其余四个进程保持一致；
	// 指标端点无需运营会话，仅暴露路由模板、状态码与耗时。
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
	// 遥测窗口、采样点数与静默会话阈值为进程内固定策略，随装配显式声明。
	control.TelemetryAPI{
		DB:           orm,
		MaxWindow:    24 * time.Hour,
		MaxPoints:    2000,
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
