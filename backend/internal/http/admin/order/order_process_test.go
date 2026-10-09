package order

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"chargepilot/internal/platform/httpapi"
)

func TestOrderProcessRejectsInvalidIdentityAndPaging(t *testing.T) {
	router := httpapi.NewRouter()
	router.GET("/orders/:id/process", (API{}).orderProcess)
	for _, path := range []string{
		"/orders/0/process", "/orders/-1/process", "/orders/not-an-id/process",
		"/orders/1/process?after_id=-1", "/orders/1/process?after_id=", "/orders/1/process?after_id=1&after_id=2",
		"/orders/1/process?after_id=18446744073709551616", "/orders/1/process?after_id=1.2",
		"/orders/1/process?limit=0", "/orders/1/process?limit=2001", "/orders/1/process?limit=", "/orders/1/process?limit=1&limit=2",
	} {
		reply := httptest.NewRecorder()
		router.ServeHTTP(reply, httptest.NewRequest(http.MethodGet, path, nil))
		if reply.Code != http.StatusBadRequest {
			t.Fatalf("%s returned %d: %s", path, reply.Code, reply.Body.String())
		}
	}
}

func TestOrderProcessRouteRequiresAuthentication(t *testing.T) {
	router := httpapi.NewRouter()
	(API{}).Register(router)
	reply := httptest.NewRecorder()
	router.ServeHTTP(reply, httptest.NewRequest(http.MethodGet, "/api/v1/admin/orders/1/process", nil))
	if reply.Code != http.StatusUnauthorized {
		t.Fatalf("process route bypassed order.read authentication: %d", reply.Code)
	}
}
