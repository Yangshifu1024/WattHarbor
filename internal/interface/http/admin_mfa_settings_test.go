package http

import (
	"encoding/json"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestIssuedIdentityIncludesDisplayNameActualRoleAndMFAState(t *testing.T) {
	jwt, err := auth.NewJWT(strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	a := AdminAuth{JWT: jwt}
	router := httpapi.NewRouter()
	router.GET("/identity", func(c *gin.Context) {
		a.issue(c, operations.Profile{ID: 7, Username: "fixture", DisplayName: "管理员显示名", Role: "custom", RoleName: "自定义中文角色", RoleID: 8, MFAEnabled: true}, "fixture-session", "fixture-refresh")
	})
	reply := httptest.NewRecorder()
	router.ServeHTTP(reply, httptest.NewRequest(http.MethodGet, "/identity", nil))
	var envelope struct {
		Data operations.Profile `json:"data"`
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &envelope); err != nil || reply.Code != 200 || envelope.Data.DisplayName != "管理员显示名" || envelope.Data.RoleName != "自定义中文角色" || envelope.Data.RoleID != 8 || !envelope.Data.MFAEnabled {
		t.Fatalf("issued identity omitted live account metadata: status=%d err=%v", reply.Code, err)
	}
}
