package http

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	chargestop "github.com/ChargePilot2026/charge-pilot/internal/charging/stop"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type stopAuthorizationStub struct{ err error }

func (s stopAuthorizationStub) Stop(context.Context, string, uint64) (chargestop.ActiveStopAuthorization, error) {
	return chargestop.ActiveStopAuthorization{}, s.err
}

func TestStopAuthorizationKeepsErrorContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		err     error
		status  int
		message string
	}{
		{gorm.ErrRecordNotFound, 409, "order is not charging for this user"},
		{chargestop.ErrPortIdentity, 409, "port identity unavailable"},
		{chargestop.ErrBillingCutoffUnavailable, 503, "计费截止暂时无法保存"},
		{errors.New("private SQL error"), 503, "order storage unavailable"},
	} {
		r := gin.New()
		StopAuthorization{Service: stopAuthorizationStub{err: test.err}, ServiceToken: "secret"}.Register(r)
		req := httptest.NewRequest("GET", "/api/v1/internal/charge-orders/C-1/stop-authorization?user_id=1", nil)
		req.Header.Set("X-Service-Token", "secret")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != test.status || !strings.Contains(w.Body.String(), test.message) || strings.Contains(w.Body.String(), "private SQL error") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
}
