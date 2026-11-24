//go:build integration

package stop

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"chargepilot/internal/gateway/control"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/platform/dbconn"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/protocol"
)

// TestGatewayEvidenceReachesNoPowerDecision 贯通真库事件、实际注册路由、客户端和停机判断。
// gateway 数据使用外层事务回滚；HTTP 为 loopback，未替代设备实机验收。
func TestGatewayEvidenceReachesNoPowerDecision(t *testing.T) {
	raw := os.Getenv("DATABASE_URL_GATEWAY")
	if raw == "" {
		u, err := url.Parse(os.Getenv("TEST_MYSQL_URL"))
		if err != nil || u.Host == "" {
			t.Fatal("gateway 测试连接未配置")
		}
		u.Path = "/gateway_db"
		raw = u.String()
	}
	db, err := dbconn.Open(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	tx := orm.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	router := httpapi.NewRouter()
	(control.DeviceEventAPI{DB: tx, ServiceToken: "evidence-test"}).Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "evidence-test")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	// 超过端点每页 10080 行，在第二页首条缺失端口，验证跨页缺失端口会中断零功率计时。
	boundarySeconds := make([]int, 10084)
	for i := range boundarySeconds {
		boundarySeconds[i] = (i-len(boundarySeconds)+1)*15 - 2
	}
	for _, tc := range []struct {
		name string
		// seconds 相对本轮检查时刻的心跳接收偏移，单位秒，负数表示过去；顺序用于验证跨页心跳连续性。
		seconds []int
		// missing 应从样本中省略目标端口的索引；负一表示全部样本具有目标端口。
		missing int
		// want 该组心跳是否应满足连续一分钟零功率的预期结果。
		want bool
	}{
		{"continuous", []int{-70, -55, -40, -25, -10, -2}, -1, true},
		{"missing", []int{-70, -55, -40, -25, -10, -2}, 2, false},
		{"reconnect", []int{-70, -2}, -1, false},
		{"out-of-order", []int{-70, -40, -55, -25, -10, -2}, -1, false},
		{"page-boundary-missing", boundarySeconds, 10080, false},
		{"page-boundary-continuous", boundarySeconds, -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			device := "EVIDENCE-" + rand.Text()
			rows := make([]map[string]any, 0, len(tc.seconds))
			for i, sec := range tc.seconds {
				ports := []protocol.PortTelemetry{{Port: 1}}
				if i == tc.missing {
					ports = []protocol.PortTelemetry{{Port: 2}}
				}
				at := now.Add(time.Duration(sec) * time.Second)
				e := protocol.Event{Type: protocol.Heartbeat, DeviceID: device, ReceivedAt: at, ChargingPorts: ports}
				payload, err := json.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				rows = append(rows, map[string]any{"event_key": rand.Text(), "protocol_name": "evidence-test", "device_id": device, "event_type": "heartbeat", "event_json": string(payload), "received_at": at})
			}
			if err := tx.Table("device_event").CreateInBatches(rows, 200).Error; err != nil {
				t.Fatal(err)
			}
			stopper := AutoStopper{Gateway: client}
			out, err := stopper.evidence(context.Background(), []AutoStopOrder{{ID: 1, DeviceID: device, PortNo: 1, StartedAt: now.Add(time.Duration(tc.seconds[0]-15) * time.Second)}})
			if err != nil {
				t.Fatal(err)
			}
			if len(out[1]) != len(tc.seconds) {
				t.Fatalf("lost heartbeat facts: %d", len(out[1]))
			}
			if got := noPowerForMinute(out[1], 1, now); got != tc.want {
				t.Fatalf("decision=%v want=%v samples=%+v", got, tc.want, out[1])
			}
		})
	}
}
