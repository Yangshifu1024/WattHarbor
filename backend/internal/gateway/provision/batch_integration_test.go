//go:build integration

package provision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
	contract "wattharbor/internal/contracts/gateway"
	"wattharbor/internal/gateway/repo"
	client "wattharbor/internal/integration/gateway"
	"wattharbor/internal/platform/dbconn"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
)

// provisionFixture 打开真实网关库，创建私有启用厂商与随机设备名；退出只清理自身身份并核对零残留。
func provisionFixture(t *testing.T) (*gorm.DB, repo.Vendor, []contract.ProvisionDevice) {
	t.Helper()
	raw := os.Getenv("DATABASE_URL_GATEWAY")
	if raw == "" {
		t.Fatal("DATABASE_URL_GATEWAY required")
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
	tag := strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	vendor := repo.Vendor{VendorCode: "PB" + tag, VendorName: "私有开通厂商", AdapterClass: "dc589", Protocol: "tcp", Status: "enabled"}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatal(err)
	}
	empty := ""
	devices := []contract.ProvisionDevice{
		{DeviceID: "PB" + tag + "1", VendorID: vendor.ID, StationID: 71, PortCount: 2},
		{DeviceID: "PB" + tag + "2", VendorID: vendor.ID, StationID: 71, PortCount: 1, Model: &empty},
		{DeviceID: "PB" + tag + "3", VendorID: vendor.ID, StationID: 71, PortCount: 1},
	}
	t.Cleanup(func() {
		for _, d := range devices {
			for _, table := range []string{"device_port", "device_provision", "device"} {
				if err := db.Table(table).Where("device_id=?", d.DeviceID).Delete(map[string]any{}).Error; err != nil {
					t.Error(err)
				}
				var n int64
				if err := db.Table(table).Where("device_id=?", d.DeviceID).Count(&n).Error; err != nil || n != 0 {
					t.Errorf("remaining %s=%d err=%v", table, n, err)
				}
			}
		}
		if err := db.Table("vendor").Where("id=?", vendor.ID).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
		}
		var n int64
		if err := db.Table("vendor").Where("id=?", vendor.ID).Count(&n).Error; err != nil || n != 0 {
			t.Errorf("remaining vendor=%d err=%v", n, err)
		}
	})
	return db, vendor, devices
}

// provisionCall 经实际 Register 同步调用开通请求，返回 HTTP 及业务响应，不启动后台任务。
func provisionCall(t *testing.T, db *gorm.DB, ctx context.Context, devices []contract.ProvisionDevice) (int, httpapi.Envelope) {
	t.Helper()
	raw, err := json.Marshal(contract.ProvisionRequest{Devices: devices})
	if err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter()
	(API{DB: db, ServiceToken: vendorTestToken}).Register(router)
	req := httptest.NewRequest(http.MethodPost, contract.ProvisionPath, strings.NewReader(string(raw))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Token", vendorTestToken)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var reply httpapi.Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	return w.Code, reply
}

// TestProvisionBatchKeepsRollbackIdentityAndReplay 在首个真实端口写入后取消，批次不得留下设备、端口或去重标识。
// 恢复通过专用客户端，再核对身份重放、NULL 型号与空型号的区别，以及后项冲突时前项整体回滚。
func TestProvisionBatchKeepsRollbackIdentityAndReplay(t *testing.T) {
	db, _, devices := provisionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var once sync.Once
	hook := "test:provision-cancel-" + uuid.NewString()
	if err := db.Callback().Create().After("gorm:commit_or_rollback_transaction").Register(hook, func(q *gorm.DB) {
		if q.Statement.Table == "device_port" && q.Error == nil && q.RowsAffected == 1 {
			once.Do(cancel)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Create().Remove(hook)
	status, reply := provisionCall(t, db, ctx, devices[:2])
	if status != 503 || reply.Code != 5003 || reply.Message != "设备配置暂时不可用" || reply.Data != nil {
		t.Fatalf("cancel reply=%d %+v", status, reply)
	}
	if err := db.Callback().Create().Remove(hook); err != nil {
		t.Fatal(err)
	}
	assertRows := func(ids []string, deviceN, identityN, portN int64) {
		t.Helper()
		for table, want := range map[string]int64{"device": deviceN, "device_provision": identityN, "device_port": portN} {
			var n int64
			if err := db.Table(table).Where("device_id IN ?", ids).Count(&n).Error; err != nil || n != want {
				t.Fatalf("atomic %s count=%d want=%d err=%v", table, n, want, err)
			}
		}
	}
	ids := []string{devices[0].DeviceID, devices[1].DeviceID}
	assertRows(ids, 0, 0, 0)
	router := httpapi.NewRouter()
	(API{DB: db, ServiceToken: vendorTestToken}).Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	edge, err := client.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, vendorTestToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := edge.Provision(context.Background(), devices[:2]); err != nil {
		t.Fatal("recovery", err)
	}
	assertRows(ids, 2, 2, 3)
	for _, d := range devices[:2] {
		var frozen struct {
			RequestJSON string `gorm:"column:request_json"` // 首次保存的标识的 JSON
		}
		if err := db.Table("device_provision").Where("device_id=?", d.DeviceID).Take(&frozen).Error; err != nil {
			t.Fatal(err)
		}
		var original contract.ProvisionDevice
		if err := json.Unmarshal([]byte(frozen.RequestJSON), &original); err != nil || original.DeviceID != d.DeviceID || original.StationID != d.StationID || original.VendorID != d.VendorID || original.PortCount != d.PortCount || (original.Model == nil) != (d.Model == nil) || (original.Model != nil && *original.Model != *d.Model) {
			t.Fatalf("identity=%+v want=%+v err=%v", original, d, err)
		}
	}
	status, reply = provisionCall(t, db, context.Background(), devices[:2])
	count, ok := reply.Data.(map[string]any)
	if status != 200 || reply.Code != 0 || !ok || count["provisioned"] != float64(2) {
		t.Fatalf("replay reply=%d %+v", status, reply)
	}
	assertRows(ids, 2, 2, 3)
	changed := devices[0]
	changed.Model = devices[1].Model
	status, reply = provisionCall(t, db, context.Background(), []contract.ProvisionDevice{devices[2], changed})
	if status != 409 || reply.Code != 2009 || reply.Message != "设备已存在且参数不同，或厂商无效（dc589 最多 20 路）" || reply.Data != nil {
		t.Fatalf("identity conflict=%d %+v", status, reply)
	}
	assertRows([]string{devices[2].DeviceID}, 0, 0, 0)
	assertRows(ids, 2, 2, 3)
	duplicate := devices[0]
	duplicate.DeviceID = strings.ToLower(duplicate.DeviceID)
	status, reply = provisionCall(t, db, context.Background(), []contract.ProvisionDevice{devices[0], duplicate})
	if status != 400 || reply.Message != "invalid device parameters" {
		t.Fatalf("case duplicate=%d %+v", status, reply)
	}
	tooMany := devices[2]
	tooMany.PortCount = 21
	status, reply = provisionCall(t, db, context.Background(), []contract.ProvisionDevice{tooMany})
	if status != 409 || reply.Code != 2009 {
		t.Fatalf("dc589 budget=%d %+v", status, reply)
	}
	assertRows([]string{devices[2].DeviceID}, 0, 0, 0)
}
