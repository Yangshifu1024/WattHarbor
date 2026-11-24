package central

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	contract "wattharbor/internal/contracts/central"
	"wattharbor/internal/platform/serviceclient"
)

// TestAlertTriggerOwnsDeadlineWithoutChangingPool 验证告警触发请求可设置独立超时，仍共享连接池并响应调用方取消。
func TestAlertTriggerOwnsDeadlineWithoutChangingPool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != contract.DeviceAlertSyncPath || r.Method != "POST" || r.Header.Get("X-Service-Token") != "alert-client-token" {
			t.Error("original trigger changed")
		}
		timer := time.NewTimer(20 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			_, _ = w.Write([]byte(`{"code":0,"data":{"raised":0}}`))
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	pool := *server.Client()
	pool.Timeout = time.Nanosecond
	client, err := NewClient(serviceclient.Client{HTTP: &pool}, server.URL, "alert-client-token")
	if err != nil {
		t.Fatal(err)
	}
	n, err := client.SyncDeviceAlerts(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("trigger=%d %v", n, err)
	}
	if pool.Timeout != time.Nanosecond || pool.Transport != server.Client().Transport {
		t.Fatal("caller pool changed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.SyncDeviceAlerts(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// TestAlertTriggerRejectsMissingProgressAndRedirect 验证 raised 必须存在且非负，缺字段或重定向不能算同步成功。
func TestAlertTriggerRejectsMissingProgressAndRedirect(t *testing.T) {
	for _, body := range []string{`{"code":0,"data":{}}`, `{"code":0,"data":{"raised":-1}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		client, err := NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "token")
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.SyncDeviceAlerts(context.Background())
		server.Close()
		if !errors.Is(err, ErrInvalidResponse) {
			t.Fatal(body, err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/redirect", http.StatusFound) }))
	defer server.Close()
	client, err := NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SyncDeviceAlerts(context.Background()); err == nil {
		t.Fatal("redirect accepted")
	}
}
