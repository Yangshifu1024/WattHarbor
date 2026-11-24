package monolith

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPublicRouterReadinessAndIsolation(t *testing.T) {
	var accepting, synchronized atomic.Bool
	role := http.NewServeMux()
	role.HandleFunc("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	role.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "role metrics")
	})
	role.HandleFunc("GET /api/docs/admin.openapi.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "admin OpenAPI")
	})
	role.HandleFunc("/api/v1/internal/private", func(w http.ResponseWriter, r *http.Request) {
		t.Error("public router reached an internal endpoint")
	})
	handler := publicRouter(role, role, map[string]http.Handler{"worker": role}, &accepting, &synchronized)
	check := func(path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, path, nil))
		if resp.Code != status {
			t.Errorf("GET %s status = %d, want %d", path, resp.Code, status)
		}
		return resp
	}
	check("/health/live", http.StatusOK)
	accepting.Store(true)
	check("/health/ready", http.StatusServiceUnavailable)
	synchronized.Store(true)
	check("/health/ready", http.StatusOK)
	check("/api/v1/internal/private", http.StatusNotFound)
	check("/api/docs/admin.openapi.json", http.StatusOK)
	check("/api/docs/other.openapi.json", http.StatusNotFound)
	if resp := check("/metrics/worker", http.StatusOK); resp.Body.String() != "role metrics" {
		t.Errorf("worker metrics = %q", resp.Body.String())
	}
	accepting.Store(false)
	check("/health/ready", http.StatusServiceUnavailable)
}

func TestLocalTransportRejectsTruncatedResponse(t *testing.T) {
	transport := localTransport{"worker": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", (4<<20)+1))
	})}
	req := httptest.NewRequest(http.MethodGet, "http://worker/api/v1/internal/diagnostics", nil)
	resp, err := transport.RoundTrip(req)
	if resp != nil || err == nil {
		t.Errorf("expected rejected oversized response, got response %v and error %v", resp, err)
	}
}
