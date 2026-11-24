package user

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"wattharbor/internal/identity"
	"wattharbor/internal/payment"
	"wattharbor/internal/platform/config"
)

// TestUserAssemblyRegistersDocumentedRoutes 对照 OpenAPI 检查模拟模式下的 C 端路由，并确认未注册其他业务族的路由。
func TestUserAssemblyRegistersDocumentedRoutes(t *testing.T) {
	router := newRouter(config.User{LoginMode: "development", PaymentMode: "simulation"}, &resources{}, nil, identity.MiniProgram{}, payment.PaymentChannel{Prepay: payment.Simulator{}}, nil)
	actual := map[string]bool{}
	for _, route := range router.Routes() {
		actual[route.Method+" "+route.Path] = true
		if strings.HasPrefix(route.Path, "/api/v1/") && !strings.HasPrefix(route.Path, "/api/v1/user/") {
			t.Fatalf("user mounted another process's route: %s", route.Path)
		}
	}
	data, err := os.ReadFile("../../http/user/user.openapi.json")
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
