package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wattharbor/internal/platform/httpapi"
)

func TestSelfMFASettingsRejectsClientSecretsAndInvalidProofs(t *testing.T) {
	router := httpapi.NewRouter()
	router.POST("/settings", (AdminAuth{}).mfaSettings)
	for _, input := range []string{
		`{}`, `{"action":"unknown"}`, `{"action":"enrol"}`, `{"action":"enrol","password":"password","secret":"chosen-secret"}`,
		`{"action":"enrol","password":"password","admin_user_id":9}`, `{"action":"enrol","password":"password","code":"123456"}`,
		`{"action":"confirm","enrollment_id":"short","code":"123456"}`, `{"action":"confirm","enrollment_id":"` + strings.Repeat("x", 43) + `","code":"abcdef"}`,
		`{"action":"disable","password":"password"}`, `{"action":"disable","password":"password","code":"12345"}`,
	} {
		reply := httptest.NewRecorder()
		router.ServeHTTP(reply, httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(input)))
		if reply.Code != http.StatusBadRequest {
			t.Fatalf("invalid MFA setting accepted: %d", reply.Code)
		}
	}
}

func TestSelfMFASettingsRequiresSession(t *testing.T) {
	router := httpapi.NewRouter()
	(AdminAuth{}).Register(router)
	reply := httptest.NewRecorder()
	router.ServeHTTP(reply, httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/mfa-settings", strings.NewReader(`{"action":"enrol","password":"password"}`)))
	if reply.Code != http.StatusUnauthorized {
		t.Fatalf("MFA settings bypassed session: %d", reply.Code)
	}
}
