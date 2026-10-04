package http

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	chargestart "github.com/ChargePilot2026/charge-pilot/internal/charging/start"
	"github.com/gin-gonic/gin"
)

type paidStartsStub struct {
	after uint64
	calls int
	err   error
}

func (s *paidStartsStub) PaidStarts(_ context.Context, after uint64) ([]chargestart.PaidChargeOrder, error) {
	s.after, s.calls = after, s.calls+1
	if after == 9 {
		return []chargestart.PaidChargeOrder{{ID: 10, OrderNo: "C-10"}}, s.err
	}
	return nil, s.err
}

func TestPaidStartsHTTPContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		query, token  string
		err           error
		status, calls int
		body          string
	}{
		{"", "", nil, 401, 0, "service token invalid"},
		{"?after_id=-1", "secret", nil, 400, 0, "invalid paid start cursor"},
		{"?after_id=18446744073709551616", "secret", nil, 400, 0, "invalid paid start cursor"},
		{"?after_id=9", "secret", nil, 200, 1, `"items":[{"id":10,"order_no":"C-10"}]`},
		{"", "secret", nil, 200, 1, `"items":[]`},
		{"", "secret", errors.New("private SQL error"), 503, 1, "paid orders unavailable"},
	} {
		s := &paidStartsStub{err: test.err}
		r := gin.New()
		PaidStartsAPI{Service: s, ServiceToken: "secret"}.Register(r)
		request := httptest.NewRequest("GET", "/api/v1/internal/charge-orders/paid-starts"+test.query, nil)
		request.Header.Set("X-Service-Token", test.token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, request)
		if w.Code != test.status || s.calls != test.calls || !strings.Contains(w.Body.String(), test.body) || strings.Contains(w.Body.String(), "private SQL error") {
			t.Fatalf("query=%s status=%d calls=%d body=%s", test.query, w.Code, s.calls, w.Body.String())
		}
		if test.query == "?after_id=9" && s.after != 9 {
			t.Fatalf("cursor=%d", s.after)
		}
	}
}
