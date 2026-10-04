package charge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestPaidStarterRetriesAfterFullScanAndKeepsCursorOnListFailure(t *testing.T) {
	var cursors []string
	listCalls, startCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "secret" {
			t.Error("missing service authentication")
		}
		switch r.URL.Path {
		case "/api/v1/internal/charge-orders/paid-starts":
			if r.Method != "GET" {
				t.Error("list must use GET")
			}
			cursors = append(cursors, r.URL.Query().Get("after_id"))
			listCalls++
			switch listCalls {
			case 1, 4:
				fmt.Fprint(w, `{"code":0,"data":{"items":[{"id":7,"order_no":"C-7"}]}}`)
			case 2:
				w.WriteHeader(503)
			case 3:
				fmt.Fprint(w, `{"code":0,"data":{"items":[]}}`)
			}
		case "/api/v1/internal/charge-orders/start":
			if r.Method != "POST" {
				t.Error("start must use POST")
			}
			startCalls++
			if startCalls == 1 {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(202)
			fmt.Fprint(w, `{"code":0,"data":{"order_no":"C-7","status":"accepted"}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	s := PaidStarter{CentralURL: server.URL, GatewayURL: server.URL, ServiceToken: "secret"}
	for i, want := range []struct {
		count int
		fail  bool
	}{{0, true}, {0, true}, {0, false}, {1, false}} {
		got, err := s.DispatchBatch(context.Background())
		if got != want.count || (err != nil) != want.fail {
			t.Fatalf("batch %d: count=%d err=%v", i, got, err)
		}
	}
	if !reflect.DeepEqual(cursors, []string{"0", "7", "7", "0"}) || startCalls != 2 {
		t.Fatalf("cursors=%v starts=%d", cursors, startCalls)
	}
}

func TestPaidStarterRejectsMalformedListBeforeDispatch(t *testing.T) {
	for _, reply := range []string{
		`{"data":{"items":[]}}`,
		`{"code":0,"data":{"items":null}}`,
		`{"code":0,"data":{"items":[{"id":5,"order_no":"C-5"}]}}`,
		`{"code":0,"data":{"items":[{"id":7,"order_no":""}]}}`,
		`{"code":0,"data":{"items":[{"id":8,"order_no":"C-8"},{"id":7,"order_no":"C-7"}]}}`,
	} {
		starts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				starts++
				w.WriteHeader(202)
				return
			}
			fmt.Fprint(w, reply)
		}))
		s := PaidStarter{CentralURL: server.URL, GatewayURL: server.URL, ServiceToken: "secret", cursor: 5}
		n, err := s.DispatchBatch(context.Background())
		server.Close()
		if err == nil || n != 0 || starts != 0 || s.cursor != 5 {
			t.Fatalf("reply=%s count=%d starts=%d cursor=%d err=%v", reply, n, starts, s.cursor, err)
		}
	}
}
