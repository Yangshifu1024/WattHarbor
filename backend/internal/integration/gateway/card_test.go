package gateway

import (
	"context"
	"errors"
	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestCardClientsRequireDecisionFields 核对冻结／开通缺字段不成为正常 false，原幂等 false 仍可恢复。
func TestCardClientsRequireDecisionFields(t *testing.T) {
	for _, tc := range []struct {
		name   string // 用例身份
		body   string // 原 HTTP 响应
		freeze bool   // 是否请求冻结端点
		valid  bool   // 是否符合原契约
	}{
		{"missing stored", `{"code":0,"data":{"response_json":"{}"}}`, true, false},
		{"null stored", `{"code":0,"data":{"stored":null,"response_json":"{}"}}`, true, false},
		{"empty decision", `{"code":0,"data":{"stored":true,"response_json":""}}`, true, false},
		{"existing decision", `{"code":0,"data":{"stored":false,"response_json":"{}"}}`, true, true},
		{"missing found", `{"code":0,"data":{}}`, false, false},
		{"missing port code", `{"code":0,"data":{"found":true}}`, false, false},
		{"not provisioned", `{"code":0,"data":{"found":false}}`, false, true},
		{"provisioned", `{"code":0,"data":{"found":true,"port_code":"original"}}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Service-Token") != "token" {
					t.Error("missing token")
				}
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := NewClient(serviceclient.Client{}, server.URL, "token")
			if err != nil {
				t.Fatal(err)
			}
			if tc.freeze {
				_, err = client.FreezeCardResponse(context.Background(), contract.CardFreezeRequest{EventKey: "e84a43ca-51a0-4bc3-b15f-ef186cf6bf28", ResponseJSON: "{}"})
			} else {
				_, err = client.ResolveCardPort(context.Background(), "device/one", 2)
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if !tc.valid && !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("contract error=%v", err)
			}
		})
	}
}

// TestCardClientsNeverFollowRedirectOrIgnoreCancellation 核对重定向不泄漏令牌，取消终止原事件请求。
func TestCardClientsNeverFollowRedirectOrIgnoreCancellation(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CardEvents(context.Background()); err == nil || redirected.Load() != 0 {
		t.Fatalf("redirect count=%d err=%v", redirected.Load(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.CardEvents(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}
