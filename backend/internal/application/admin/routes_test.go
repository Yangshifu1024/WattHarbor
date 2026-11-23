package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/platform/config"
	"github.com/gin-gonic/gin"
)

// TestAdminTrustedProxyBoundary 使用内存请求核对单一可信代理、伪造头及空/无效配置，不代表生产验收。
func TestAdminTrustedProxyBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string   // 配置及来源场景
		trusted   []string // 显式可信代理
		remote    string   // 直连来源
		forwarded string   // 请求中的转发来源
		want      string   // Gin 识别的客户端地址
	}{
		{"trusted client A", []string{"172.30.81.2"}, "172.30.81.2:5000", "203.0.113.1", "203.0.113.1"},
		{"trusted client B", []string{"172.30.81.2"}, "172.30.81.2:5000", "203.0.113.2", "203.0.113.2"},
		{"untrusted spoof", []string{"172.30.81.2"}, "172.30.81.4:5000", "203.0.113.1", "172.30.81.4"},
		{"empty config", nil, "172.30.81.2:5000", "203.0.113.1", "172.30.81.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := newRouter(config.Admin{TrustedProxies: tc.trusted}, &resources{}, authhttp.AdminAuth{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			r.GET("/test-client-ip", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })
			q := httptest.NewRequest(http.MethodGet, "/test-client-ip", nil)
			q.RemoteAddr = tc.remote
			q.Header.Set("X-Forwarded-For", tc.forwarded)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, q)
			if w.Body.String() != tc.want {
				t.Fatalf("IP=%s want=%s", w.Body.String(), tc.want)
			}
		})
	}
	if _, err := newRouter(config.Admin{TrustedProxies: []string{"invalid"}}, &resources{}, authhttp.AdminAuth{}, nil); err == nil {
		t.Fatal("invalid config accepted")
	}
}

// TestAdminAssemblyRegistersDocumentedRoutes 对照 OpenAPI 检查后台路由，并确认未注册其他业务族的路由；不发送业务请求。
func TestAdminAssemblyRegistersDocumentedRoutes(t *testing.T) {
	router, err := newRouter(config.Admin{}, &resources{}, authhttp.AdminAuth{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	actual := map[string]bool{}
	for _, route := range router.Routes() {
		actual[route.Method+" "+route.Path] = true
		if strings.HasPrefix(route.Path, "/api/v1/") && !strings.HasPrefix(route.Path, "/api/v1/admin/") {
			t.Fatalf("admin mounted another process's route: %s", route.Path)
		}
	}
	data, err := os.ReadFile("../../http/auth/admin.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	parameters := regexp.MustCompile(`\{([^}]+)\}`)
	for path, operations := range doc.Paths {
		for method := range operations {
			switch method {
			case "get", "post", "put", "patch", "delete", "options", "head":
			default:
				continue
			}
			key := strings.ToUpper(method) + " " + parameters.ReplaceAllString(path, ":$1")
			if !actual[key] {
				t.Errorf("documented route not mounted: %s", key)
			}
		}
	}
}
