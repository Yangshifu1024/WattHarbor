package httpapi

import (
	"database/sql"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics 每个服务各持有一个 registry，并使用匹配到的 Gin 路由模板，
// 这样即使 URL 里含有设备 ID，时间序列的基数也是有界的。
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	handler  http.Handler
}

// DatabasePool 将进程持有的 SQL 池关联到固定的 schema 标签。
// 调用方只传部署定义的 schema 名，不传请求参数或数据库 URL。
type DatabasePool struct {
	Schema string
	DB     *sql.DB
}

func NewMetrics(service string, pools ...DatabasePool) *Metrics {
	registry := prometheus.NewRegistry()
	started := time.Now()
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "chargepilot_http_requests_total", Help: "Handled HTTP requests.",
	}, []string{"method", "route", "status"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "chargepilot_http_request_duration_seconds", Help: "HTTP request latency in seconds.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"method", "route"})
	registry.MustRegister(requests, duration,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "chargepilot_service_info", Help: "Always 1; the service this scrape came from.",
			ConstLabels: prometheus.Labels{"service": service},
		}, func() float64 { return 1 }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "chargepilot_build_info", Help: "Build and start information.",
			ConstLabels: prometheus.Labels{"service": service, "go_version": runtime.Version()},
		}, func() float64 { return 1 }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "chargepilot_uptime_seconds", Help: "Seconds since this process started.",
			ConstLabels: prometheus.Labels{"service": service},
		}, func() float64 { return time.Since(started).Seconds() }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "chargepilot_goroutines", Help: "Number of goroutines.",
			ConstLabels: prometheus.Labels{"service": service},
		}, func() float64 { return float64(runtime.NumGoroutine()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "chargepilot_heap_in_use_bytes", Help: "Bytes of heap in use.",
			ConstLabels: prometheus.Labels{"service": service},
		}, func() float64 { var mem runtime.MemStats; runtime.ReadMemStats(&mem); return float64(mem.HeapInuse) }),
	)
	for _, pool := range pools {
		if pool.DB != nil {
			registerPoolMetrics(registry, service, pool)
		}
	}
	return &Metrics{requests: requests, duration: duration, handler: promhttp.HandlerFor(registry, promhttp.HandlerOpts{})}
}

func (m *Metrics) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		begin := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		m.requests.WithLabelValues(c.Request.Method, route, strconv.Itoa(c.Writer.Status())).Inc()
		m.duration.WithLabelValues(c.Request.Method, route).Observe(time.Since(begin).Seconds())
	}
}

func (m *Metrics) Register(router *gin.Engine) {
	router.GET("/metrics", func(c *gin.Context) { m.handler.ServeHTTP(c.Writer, c.Request) })
}

// registerPoolMetrics 在当前服务注册表采集单池连接使用、等待和回收统计。
func registerPoolMetrics(registry *prometheus.Registry, service string, pool DatabasePool) {
	labels := prometheus.Labels{"service": service, "schema": pool.Schema}
	for _, metric := range []struct {
		name  string
		help  string
		value func(sql.DBStats) float64
	}{
		{"open_connections", "Number of open SQL connections.", func(s sql.DBStats) float64 { return float64(s.OpenConnections) }},
		{"in_use_connections", "Number of SQL connections in use.", func(s sql.DBStats) float64 { return float64(s.InUse) }},
		{"idle_connections", "Number of idle SQL connections.", func(s sql.DBStats) float64 { return float64(s.Idle) }},
		{"max_open_connections", "Configured SQL connection limit; zero means unlimited.", func(s sql.DBStats) float64 { return float64(s.MaxOpenConnections) }},
	} {
		registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "chargepilot_db_" + metric.name, Help: metric.help, ConstLabels: labels}, func() float64 { return metric.value(pool.DB.Stats()) }))
	}
	for _, metric := range []struct {
		name  string
		help  string
		value func(sql.DBStats) float64
	}{
		{"wait_total", "Number of waits for a SQL connection.", func(s sql.DBStats) float64 { return float64(s.WaitCount) }},
		{"wait_seconds_total", "Total time waiting for SQL connections.", func(s sql.DBStats) float64 { return s.WaitDuration.Seconds() }},
		{"max_idle_closed_total", "SQL connections closed by the idle connection limit.", func(s sql.DBStats) float64 { return float64(s.MaxIdleClosed) }},
		{"max_idle_time_closed_total", "SQL connections closed by the idle time limit.", func(s sql.DBStats) float64 { return float64(s.MaxIdleTimeClosed) }},
		{"max_lifetime_closed_total", "SQL connections closed by the lifetime limit.", func(s sql.DBStats) float64 { return float64(s.MaxLifetimeClosed) }},
	} {
		registry.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "chargepilot_db_" + metric.name, Help: metric.help, ConstLabels: labels}, func() float64 { return metric.value(pool.DB.Stats()) }))
	}
}
