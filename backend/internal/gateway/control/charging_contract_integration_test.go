//go:build integration

package control

import (
	"context"
	"crypto/rand"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/platform/dbconn"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"gorm.io/gorm"
)

// gatewayContractDB 优先使用 DATABASE_URL_GATEWAY；否则将 TEST_MYSQL_URL 的库名改为 gateway_db。
// 连接超时为 10 秒；连接失败终止测试，已打开的连接池由 t.Cleanup 关闭。
func gatewayContractDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("DATABASE_URL_GATEWAY")
	if raw == "" {
		raw = os.Getenv("TEST_MYSQL_URL")
		if raw == "" {
			t.Fatal("DATABASE_URL_GATEWAY 或 TEST_MYSQL_URL 未设置")
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal("测试数据库 URL 无效")
		}
		parsed.Path = "/gateway_db"
		raw = parsed.String()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := dbconn.Open(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}

// 通过实际网关路由和真实数据库验证客户端契约；随机设备无记录，不写测试数据。
func TestChargingEvidenceClientMatchesRegisteredGateway(t *testing.T) {
	orm := gatewayContractDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	router := httpapi.NewRouter()
	(DeviceEventAPI{DB: orm, ServiceToken: "contract-test-token"}).Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "contract-test-token")
	if err != nil {
		t.Fatal(err)
	}
	device := "CONTRACT-" + rand.Text()
	pages, err := client.ChargingEvidence(ctx, []gatewaycontract.EvidenceQueryRequest{{DeviceID: device, PortNo: 1, StartedAt: time.Now().UTC().Add(-time.Minute)}})
	if err != nil || len(pages) != 1 {
		t.Fatalf("pages=%+v err=%v", pages, err)
	}
	page := pages[0]
	if page.DeviceID != device || page.PortNo != 1 || !page.Complete || page.NextAfterID != 0 || page.Samples == nil || len(page.Samples) != 0 {
		t.Fatalf("unexpected gateway response: %+v", page)
	}
}
