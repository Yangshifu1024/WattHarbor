package station

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateStationRejectsRetiredOpeningHours(t *testing.T) {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/stations", strings.NewReader(`{"name":"全年无休站点","longitude":111,"latitude":30,"status":"active","open_hours":"00:00-24:00"}`))
	context.Request.Header.Set("Content-Type", "application/json")
	// Unknown fields must fail before the handler accesses the database.
	API{}.createStation(context)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("retired opening hours accepted: status=%d body=%s", response.Code, response.Body.String())
	}
}
