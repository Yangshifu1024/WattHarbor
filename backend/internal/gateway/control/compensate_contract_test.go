package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gatewayclient "wattharbor/internal/integration/gateway"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
)

type compensationContractService struct {
	orderNo, commandID string
	calls              int
}

func (s *compensationContractService) Request(_ context.Context, orderNo, commandID string) error {
	s.orderNo, s.commandID = orderNo, commandID
	s.calls++
	return nil
}

func TestCompensationClientMatchesRegisteredGatewayRoute(t *testing.T) {
	service := &compensationContractService{}
	router := httpapi.NewRouter()
	CompensationAPI{Service: service, ServiceToken: "secret"}.Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	id := "10000000-0000-4000-8000-000000000001"
	if err := client.CompensateStart(context.Background(), "C-1", id); err != nil || service.orderNo != "C-1" || service.commandID != id || service.calls != 1 {
		t.Fatalf("service=%+v err=%v", service, err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/charge-orders/C-1/compensate", strings.NewReader(`{"command_id":"invalid"}`))
	r.Header.Set("X-Service-Token", "secret")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 400 || service.calls != 1 {
		t.Fatalf("invalid command reached compensation: status=%d calls=%d", w.Code, service.calls)
	}
}
