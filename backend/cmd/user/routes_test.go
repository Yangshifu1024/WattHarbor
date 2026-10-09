package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"chargepilot/internal/identity"
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/config"
)

// TestUserAssemblyRegistersDocumentedRoutes 使用 t 对照模拟模式 C 端装配与 OpenAPI 的方法／路径，并拒绝其他业务族；文件读取或比对失败由 t 报告，不执行支付或存储操作。
func TestUserAssemblyRegistersDocumentedRoutes(t *testing.T) {
	router := newRouter(config.User{LoginMode: "development", PaymentMode: "simulation"}, &resources{}, nil, identity.MiniProgram{}, payment.PaymentChannel{Prepay: payment.Simulator{}}, nil)
	actual := map[string]bool{}
	for _, route := range router.Routes() {
		actual[route.Method+" "+route.Path] = true
		if strings.HasPrefix(route.Path, "/api/v1/") && !strings.HasPrefix(route.Path, "/api/v1/user/") {
			t.Fatalf("user mounted another process's route: %s", route.Path)
		}
	}
	data, err := os.ReadFile("../../internal/http/user/user.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		// Paths OpenAPI 路径到方法声明的映射，仅用于注册清单比对，不执行接口。
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
