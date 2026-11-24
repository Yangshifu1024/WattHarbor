package httpapi

import (
	"database/sql"
	_ "github.com/go-sql-driver/mysql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func metricsRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	m := NewMetrics("testsvc")
	router := NewRouter(m)
	m.Register(router)
	router.GET("/api/v1/devices/:device_id/summary", func(c *gin.Context) { OK(c, gin.H{"ok": true}) })
	return router
}

func scrape(t *testing.T, router http.Handler) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", recorder.Code)
	}
	return recorder.Body.String()
}

// 验证 route 标签使用路由模板，避免设备 ID 或订单号形成大量独立时间序列。
func TestMetricsLabelRoutesByTemplateNotRawPath(t *testing.T) {
	router := metricsRouter()
	for _, id := range []string{"device-aaa", "device-bbb", "device-ccc"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/v1/devices/"+id+"/summary", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("request returned %d", recorder.Code)
		}
	}
	body := scrape(t, router)
	if strings.Contains(body, "device-aaa") || strings.Contains(body, "device-bbb") {
		t.Fatalf("metric labels leaked a device id:\n%s", body)
	}
	if !strings.Contains(body, `route="/api/v1/devices/:device_id/summary"`) {
		t.Fatalf("expected the route template label, got:\n%s", body)
	}
	// 三台不同的设备不能产生三条序列。
	if got := strings.Count(body, `route="/api/v1/devices/:device_id/summary",status="200"`); got != 1 {
		t.Fatalf("expected 1 counter series, found %d", got)
	}
}

// 未匹配路由的请求应共用一个指标标签，避免任意路径增加时间序列数量。
func TestMetricsCollapseUnmatchedRequests(t *testing.T) {
	router := metricsRouter()
	for i := range 3 {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", "/no/such/path/"+string(rune('a'+i)), nil))
	}
	body := scrape(t, router)
	if !strings.Contains(body, `route="unmatched"`) {
		t.Fatalf("expected unmatched requests to share one label, got:\n%s", body)
	}
	if strings.Contains(body, "/no/such/path") {
		t.Fatalf("unmatched raw path leaked into a label:\n%s", body)
	}
}

func TestMetricsExposeValidExposition(t *testing.T) {
	router := metricsRouter()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/v1/devices/dev-1/summary", nil))
	body := scrape(t, router)

	for _, want := range []string{
		"# TYPE chargepilot_http_requests_total counter",
		"# TYPE chargepilot_http_request_duration_seconds histogram",
		"chargepilot_service_info{service=\"testsvc\"} 1",
		"chargepilot_uptime_seconds{service=\"testsvc\"}",
		"chargepilot_goroutines{service=\"testsvc\"}",
		"chargepilot_http_request_duration_seconds_bucket",
		"le=\"+Inf\"",
		"chargepilot_http_request_duration_seconds_count",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("exposition missing %q:\n%s", want, body)
		}
	}
	// 每行指标必须携带 service 标签；HELP、TYPE 声明除外，不允许空行。
	for line := range strings.SplitSeq(strings.TrimSpace(body), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "chargepilot_") {
			t.Fatalf("unexpected exposition line: %q", line)
		}
	}
}

func TestMetricsExposePoolBudgetWithoutCredentials(t *testing.T) {
	db, err := sql.Open("mysql", "fixture:secret@tcp(localhost:3306)/central_db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(12)
	m := NewMetrics("worker", DatabasePool{Schema: "central_db", DB: db}, DatabasePool{Schema: "gateway_db"})
	router := NewRouter(m)
	m.Register(router)
	body := scrape(t, router)
	for _, want := range []string{
		`chargepilot_db_max_open_connections{schema="central_db",service="worker"} 12`,
		`chargepilot_db_wait_total{schema="central_db",service="worker"} 0`,
		`chargepilot_db_wait_seconds_total{schema="central_db",service="worker"} 0`,
		`chargepilot_db_max_idle_time_closed_total{schema="central_db",service="worker"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "localhost") || strings.Contains(body, `schema="gateway_db"`) {
		t.Fatal("pool metrics contain credentials or unopened pool")
	}
}
