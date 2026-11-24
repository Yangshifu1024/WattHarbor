//go:build integration

package query

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"chargepilot/internal/card"
	"chargepilot/internal/gateway/control"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/order"
	"chargepilot/internal/platform/dbconn"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/protocol"
	"chargepilot/internal/testfixture"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// liveDB 为 t 按 name 打开指定真实数据库，缺配置失败；只关闭本测试连接池，不建表或清库。
func liveDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		t.Fatalf("%s is required", name)
	}
	pool, err := dbconn.Open(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	db, err := dbconn.WrapGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// TestLiveUsesRegisteredGatewayAndTwoOwnedQueries 通过实际网关路由与两库验证批量心跳查询、冻结价格和刷卡累计购买。
// 21单只发两次 HTTP，中央计价读取仅两次 SQL；其它端口与断线不增加计量，全部造数外层回滚。
func TestLiveUsesRegisteredGatewayAndTwoOwnedQueries(t *testing.T) {
	central := liveDB(t, "TEST_MYSQL_URL").Begin()
	gateway := liveDB(t, "DATABASE_URL_GATEWAY").Begin()
	if central.Error != nil || gateway.Error != nil {
		t.Fatal(central.Error, gateway.Error)
	}
	defer central.Rollback()
	defer gateway.Rollback()
	start := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Minute)
	now := start.Add(90 * time.Second)
	tag := uuid.NewString()
	device := "LQ" + tag
	id := testfixture.NewID()
	raw := durationSnapshot(t)
	rows := liveRows(21, start)
	for i := range rows {
		rows[i].OrderID = id + uint64(i)
		rows[i].DeviceID = device
		if err := central.Table("charge_order_pricing").Create(map[string]any{"charge_order_id": rows[i].OrderID, "payment_intent_id": uuid.NewString(), "user_id": id, "port_code": tag, "pricing_snapshot": string(raw)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := range 2 {
		paid, minutes := int64(1200), uint16(120)
		if i == 1 {
			paid, minutes = 0, 0
		}
		if err := central.Table("card_charge").Create(map[string]any{"charge_order_id": id + uint64(i), "card_id": id + uint64(i), "port_code": tag, "paid_cents": paid, "purchased_minutes": minutes, "max_minutes": 300, "card_no": fmt.Sprint(id + uint64(i)), "wallet_after_cents": 700, "package_json": string(raw)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 4; i++ {
		at := start.Add(time.Duration(i*15) * time.Second)
		event := protocol.Event{Type: protocol.Heartbeat, DeviceID: device, ReceivedAt: at, ChargingPorts: []protocol.PortTelemetry{{Port: 1, ChargedSeconds: uint32(i * 15), ChargedMWh: uint32(i * 1000), PowerDeciWatts: 1500}, {Port: 2, ChargedSeconds: 3600, ChargedMWh: 1000000, PowerDeciWatts: 6000}}}
		payload, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := gateway.Table("device_event").Create(map[string]any{"event_key": fmt.Sprintf("%s-%d", tag, i), "protocol_name": "dc589", "device_id": device, "event_type": "heartbeat", "event_json": string(payload), "received_at": at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	router := httpapi.NewRouter()
	var calls atomic.Int64
	router.Use(func(c *gin.Context) { calls.Add(1); c.Next() })
	control.DeviceEventAPI{DB: gateway, ServiceToken: "live-evidence-token"}.Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "live-evidence-token")
	if err != nil {
		t.Fatal(err)
	}
	var queries atomic.Int64
	const counter = "live_owned_query_count"
	if err := central.Callback().Query().Before("gorm:query").Register(counter, func(q *gorm.DB) {
		if q.Statement.Table == "charge_order_pricing" || q.Statement.Table == "card_charge" {
			queries.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer central.Callback().Query().Remove(counter)
	service := LiveService{Gateway: client, Orders: order.SnapshotRepo{DB: central}, Cards: card.PurchaseQueryRepo{DB: central}, Now: func() time.Time { return now }}
	service.Fill(context.Background(), rows)
	if calls.Load() != 2 || queries.Load() != 2 {
		t.Fatalf("HTTP=%d owned SQL=%d", calls.Load(), queries.Load())
	}
	for i, row := range rows {
		if row.Live == nil || row.Live.Seconds != 60 || row.Live.KWh != 0.004 || row.Live.Stale || row.LiveUnavailable != "" {
			t.Fatalf("row %d=%+v", i, row)
		}
		if i == 1 {
			if row.Live.Fee != nil || row.Live.FeeUnavailable != "刷卡购买记录待确认" {
				t.Fatalf("zero purchase invented fee: %+v", row.Live)
			}
		} else if row.Live.Fee == nil || row.Live.Fee.TotalCents != 10 {
			t.Fatalf("frozen fee %d=%+v", i, row.Live)
		}
	}
	queries.Store(0)
	calls.Store(0)
	service.Now = func() time.Time { return start.Add(time.Hour) }
	service.Fill(context.Background(), rows[:1])
	if rows[0].Live == nil || !rows[0].Live.Stale || rows[0].Live.Seconds != 60 || rows[0].Live.Fee.TotalCents != 10 || calls.Load() != 1 || queries.Load() != 2 {
		t.Fatal("disconnection changed original observed meter")
	}
}
