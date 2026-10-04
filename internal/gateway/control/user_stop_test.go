package control

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
)

func TestStopContractKeepsAuthenticationAndInputValidation(t *testing.T) {
	router := httpapi.NewRouter()
	(UserStopAPI{ServiceToken: "service"}).Register(router)
	for _, tc := range []struct {
		name, token, body string
		status            int
	}{
		{"no token", "", `{"order_no":"C-1","user_id":1,"source":"auto"}`, 401},
		{"wrong token", "wrong", `{}`, 401},
		{"empty body", "service", `{}`, 400},
		{"missing order", "service", `{"user_id":1,"source":"auto"}`, 400},
		{"long order", "service", `{"order_no":"` + strings.Repeat("a", 65) + `","user_id":1,"source":"auto"}`, 400},
		{"zero user", "service", `{"order_no":"C-1","user_id":0,"source":"auto"}`, 400},
		{"missing source", "service", `{"order_no":"C-1","user_id":1}`, 400},
		{"invalid source", "service", `{"order_no":"C-1","user_id":1,"source":"other"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/internal/charge-orders/stop", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Service-Token", tc.token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
		})
	}
}
