package device

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"chargepilot/internal/device"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
)

// TestDeviceStatusRejectsInvalidRequestsBeforeWriting 验证设备运营状态端点先拒绝缺值、非法状态、额外字段和多个JSON值。
func TestDeviceStatusRejectsInvalidRequestsBeforeWriting(t *testing.T) {
	for _, body := range []string{`{}`, `{"status":"fault"}`, `{"status":"enabled","station_id":9}`, `{"status":"disabled"} {}`} {
		r := httpapi.NewRouter()
		r.PUT("/devices/:id/status", (API{}).setDeviceStatus)
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest("PUT", "/devices/board/status", bytes.NewBufferString(body)))
		if response.Code != 400 {
			t.Fatalf("%s: %d", body, response.Code)
		}
	}
}

// TestDeviceEnrichmentUsesOnlyVisibleDevicesAndPreservesUnknown 通过具名loopback客户端验证只查询可见设备，保留真实零值、未知值及网关故障状态。
func TestDeviceEnrichmentUsesOnlyVisibleDevicesAndPreservesUnknown(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := r.URL.Query()["device_id"]
		if r.URL.Path != "/api/v1/internal/device-summaries" || len(ids) != 1 || ids[0] != "visible-board" || r.Header.Get("X-Service-Token") != "service" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"device_id":"visible-board","vendor_name":"测试厂商","last_heartbeat_at":"2026-10-01T03:12:34Z","signal_strength":0,"signal_at":"2026-10-01T03:12:34Z","ports":[{"port_no":1,"status_code":0,"status_at":"2026-10-01T03:12:34Z"},{"port_no":2,"status_code":null,"status_at":null}]},{"device_id":"hidden-board","vendor_name":"其他厂商","signal_strength":99,"ports":[{"port_no":9,"status_code":9}]}]}}`))
	}))
	defer gateway.Close()
	rows := []device.Device{{DeviceID: "visible-board"}}
	summaryAPI(t, gateway.URL).Management.Enrich(context.Background(), rows)
	if !rows[0].RuntimeAvailable || rows[0].VendorName == nil || *rows[0].VendorName != "测试厂商" || rows[0].LastHeartbeatAt == nil {
		t.Fatalf("missing enrichment: %+v", rows)
	}
	if rows[0].SignalStrength == nil || *rows[0].SignalStrength != 0 || rows[0].SignalAt == nil || len(rows[0].Ports) != 2 || rows[0].Ports[0].StatusCode == nil || *rows[0].Ports[0].StatusCode != 0 || rows[0].Ports[0].StatusAt == nil || rows[0].Ports[1].StatusCode != nil || rows[0].Ports[1].StatusAt != nil {
		t.Fatalf("zero/unknown device readings were changed: %+v", rows[0])
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer failing.Close()
	rows = []device.Device{{DeviceID: "visible-board"}}
	summaryAPI(t, failing.URL).Management.Enrich(context.Background(), rows)
	if rows[0].RuntimeAvailable || rows[0].LastHeartbeatAt != nil || rows[0].SignalStrength != nil || rows[0].SignalAt != nil || rows[0].Ports == nil || len(rows[0].Ports) != 0 {
		t.Fatal("outage fabricated known runtime")
	}
}

// TestDeviceEnrichmentNormalizesMissingPortArrays 验证缺失、null及未配置网关的端口均输出空数组，不补造设备运行事实。
func TestDeviceEnrichmentNormalizesMissingPortArrays(t *testing.T) {
	for _, body := range []string{`{"code":0,"data":{"items":[{"device_id":"board","ports":null}]}}`, `{"code":0,"data":{"items":[{"device_id":"board"}]}}`} {
		gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		rows := []device.Device{{DeviceID: "board"}}
		summaryAPI(t, gateway.URL).Management.Enrich(context.Background(), rows)
		gateway.Close()
		if !rows[0].RuntimeAvailable || rows[0].Ports == nil || len(rows[0].Ports) != 0 {
			t.Fatalf("missing ports must stay unknown in an empty array: %+v", rows[0])
		}
	}
	rows := []device.Device{{DeviceID: "board"}}
	(API{}).Management.Enrich(context.Background(), rows)
	encoded, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		// Ports 是公开端口数组；未知状态也必须保持非nil空数组。
		Ports []device.DevicePortStatus `json:"ports"`
	}
	if err := json.Unmarshal(encoded, &out); err != nil || out.Ports == nil || rows[0].RuntimeAvailable {
		t.Fatalf("unconfigured runtime must expose [] without fabricating availability: %s %v", encoded, err)
	}
}

// summaryAPI 为 t 在 url 的 loopback 服务注入真实具名客户端；配置错误终止，无数据库写入。
func summaryAPI(t *testing.T, url string) API {
	t.Helper()
	client, err := gatewayclient.NewClient(serviceclient.Client{}, url, "service")
	if err != nil {
		t.Fatal(err)
	}
	return API{Management: device.ManagementService{Summaries: client}}
}
