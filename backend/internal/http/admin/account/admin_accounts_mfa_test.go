package account

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chargepilot/internal/domain/operations"
	"chargepilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
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
