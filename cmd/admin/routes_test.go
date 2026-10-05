package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
)

func TestAdminAssemblyRegistersDocumentedRoutes(t *testing.T) {
	router := newRouter(config.Admin{}, &resources{}, authhttp.AdminAuth{}, nil)
	actual := map[string]bool{}
	for _, route := range router.Routes() {
		actual[route.Method+" "+route.Path] = true
		if strings.HasPrefix(route.Path, "/api/v1/") && !strings.HasPrefix(route.Path, "/api/v1/admin/") {
			t.Fatalf("admin mounted another process's route: %s", route.Path)
		}
	}
	data, err := os.ReadFile("../../internal/http/auth/admin.openapi.json")
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
