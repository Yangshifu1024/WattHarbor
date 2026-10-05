package central

import (
	"context"
	"errors"
	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCardServiceFailuresDoNotBecomeBusinessDeclines 使用 t 与本地 HTTP 服务核对原刷卡错误分类，只有明确业务拒绝携带 CardDeclineReply；服务不可用或坏回复不转成业务拒绝。验证服务令牌和余额不足标志，失败由 t 报告；关闭本地服务，不调用真实中央用例、设备或资金。
func TestCardServiceFailuresDoNotBecomeBusinessDeclines(t *testing.T) {
	for _, tc := range []struct {
		// name 原错误分类场景名，用于子测试定位。
		name string
		// status 本地服务返回的原 HTTP 状态，用于区分业务拒绝及服务失败。
		status int
		// body 本地服务返回的原响应正文，保留余额不足、空数据、服务失败及非 JSON 形状。
		body string
		// declined,balance 分别表示预期是否能分类为 ErrCardDeclined，以及有效业务拒绝回复的原余额不足标志；不是实际卡状态。
		declined, balance bool
	}{
		{"insufficient", 409, `{"code":2009,"data":{"insufficient_balance":true}}`, true, true},
		{"invalid operation", 409, `{"code":2009,"data":null}`, true, false},
		{"server unavailable", 503, `{"code":5001}`, false, false},
		{"malformed rejection", 409, `bad response`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Service-Token") != "token" {
					t.Error("missing authentication")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := NewClient(serviceclient.Client{}, server.URL, "token")
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.SwipeCard(context.Background(), contract.CardSwipeRequest{EventID: "stable"})
			if err == nil || errors.Is(err, contract.ErrCardDeclined) != tc.declined {
				t.Fatalf("%v", err)
			}
			var declined *contract.CardDeclineReply
			if tc.declined && (!errors.As(err, &declined) || declined.InsufficientBalance != tc.balance) {
				t.Fatalf("%+v", err)
			}
		})
	}
}

// TestCardBalanceRejectsIncompleteResponse 核对缺失、负数、错误业务码和无效 JSON 不被当作零余额。
func TestCardBalanceRejectsIncompleteResponse(t *testing.T) {
	for _, body := range []string{`{"code":0,"data":{}}`, `{"code":0,"data":{"valid":true}}`, `{"code":0,"data":{"valid":true,"balance_cents":-1}}`, `{"code":2009,"data":{"valid":true,"balance_cents":1}}`, `{"data":{"valid":true,"balance_cents":1}}`, `{"code":0,"data":null}`, "bad response"} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer server.Close()
			client, err := NewClient(serviceclient.Client{}, server.URL, "token")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.CardBalance(context.Background(), "17"); err == nil || errors.Is(err, contract.ErrCardDeclined) {
				t.Fatalf("incomplete balance=%v", err)
			}
		})
	}
}

// TestCardRequestKeepsRedirectAndOversizedResponsesUnknown 核对响应有界、禁止重定向和调用方取消不转成业务拒绝。
func TestCardRequestKeepsRedirectAndOversizedResponsesUnknown(t *testing.T) {
	redirected := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++ }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	client, err := NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SwipeCard(context.Background(), contract.CardSwipeRequest{}); err == nil || errors.Is(err, contract.ErrCardDeclined) || redirected != 0 {
		t.Fatalf("redirected=%d err=%v", redirected, err)
	}
	server.Close()
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(make([]byte, (64<<10)+1)) }))
	defer server.Close()
	client, err = NewClient(serviceclient.Client{}, server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SwipeCard(context.Background(), contract.CardSwipeRequest{}); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("oversized=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.SwipeCard(ctx, contract.CardSwipeRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}
