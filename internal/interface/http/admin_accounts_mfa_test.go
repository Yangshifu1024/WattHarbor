package http

import (
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdministratorMFAEndpointStillRejectsSelfOperation(t *testing.T) {
	router := httpapi.NewRouter()
	router.POST("/admin-users/:id/mfa", func(c *gin.Context) {
		c.Set("admin_profile", operations.Profile{ID: 7})
		(AdminAccounts{}).adminUserMFA(c)
	})
	reply := httptest.NewRecorder()
	router.ServeHTTP(reply, httptest.NewRequest(http.MethodPost, "/admin-users/7/mfa", strings.NewReader(`{"action":"enrol"}`)))
	if reply.Code != http.StatusConflict {
		t.Fatalf("administrator management endpoint allowed self MFA: %d", reply.Code)
	}
}
