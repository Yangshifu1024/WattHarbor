//go:build integration

package device_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/provision"
	gatewayclient "github.com/ChargePilot2026/charge-pilot/internal/integration/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// importDB 打开 env 对应的独立真库连接并由 t 关闭，配置缺失或连接失败终止，不跳过。
func importDB(t *testing.T, env string) *gorm.DB {
	t.Helper()
	url := os.Getenv(env)
	if url == "" {
		t.Fatalf("%s is required", env)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}

// TestImportRecoversGatewayCommitAndCommittedFailure 贯穿实际导入用例、具名客户端及注册 gateway 路由。
// 两库真实提交：网关成功后中央故障仍可原任务恢复；网络拒绝提交 failed 后原清单重试，完成重放不再开通。
// 仅授权身份为 fixture；所有已提交造数按唯一身份清理并检查零残留，不使用全表清理。
func TestImportRecoversGatewayCommitAndCommittedFailure(t *testing.T) {
	central := importDB(t, "TEST_MYSQL_URL")
	gateway := importDB(t, "DATABASE_URL_GATEWAY")
	id := uint64(time.Now().UnixNano())
	request, secondRequest := uuid.NewString(), uuid.NewString()
	one, two := "IM"+rand.Text(), "IN"+rand.Text()
	defer func() {
		for _, q := range []struct {
			db    *gorm.DB /* 本测试实际提交的目标库 */
			table string   /* 所属持久表 */
			where string   /* 原身份筛选 */
			arg   any      /* 本测试身份集合 */
		}{
			{central, "audit_log", "request_id IN ?", []string{request, secondRequest}}, {central, "device_import", "import_id IN ?", []string{request, secondRequest}},
			{central, "device_import_identity", "device_id IN ?", []string{one, two}}, {central, "device_meta", "device_id IN ?", []string{one, two}}, {central, "station", "id=?", id},
			{gateway, "device_port", "device_id IN ?", []string{one, two}}, {gateway, "device_provision", "device_id IN ?", []string{one, two}},
			{gateway, "device", "device_id IN ?", []string{one, two}}, {gateway, "vendor", "id=?", id},
		} {
			var cleanupErr error
			if q.table == "audit_log" {
				cleanupErr = cleanupDeviceAudits(context.Background(), q.db, []string{request, secondRequest})
			} else {
				cleanupErr = q.db.Table(q.table).Where(q.where, q.arg).Delete(nil).Error
			}
			if err := cleanupErr; err != nil {
				t.Error(err)
			}
			var n int64
			if err := q.db.Table(q.table).Where(q.where, q.arg).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("%s remaining=%d %v", q.table, n, err)
			}
		}
	}()
	if err := central.Table("station").Create(map[string]any{"id": id, "name": one, "longitude": 0, "latitude": 0, "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := gateway.Table("vendor").Create(map[string]any{"id": id, "vendor_code": one, "vendor_name": one, "adapter_class": "dc589", "protocol": "tcp", "status": "enabled"}).Error; err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter()
	token := "import-actual-service-token"
	(provision.API{DB: gateway, ServiceToken: token}).Register(router)
	var reject atomic.Bool
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == contract.ProvisionPath {
			calls.Add(1)
			if reject.Load() {
				w.WriteHeader(503)
				return
			}
		}
		router.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scope := operations.DataScope{Unrestricted: true}
	audit := ops.AuditContext{Actor: operations.Profile{ID: id, Username: "import-proof", Permissions: []string{"device.import"}}, RequestID: request}
	service := device.ImportService{DB: central, Gateway: client, Compatibility: pricing.Repo{DB: central}, LoadScope: func(ctx context.Context, p operations.Profile) (operations.DataScope, error) {
		return ops.LoadDataScope(ctx, central, p)
	}}
	in := device.ImportInput{ImportID: request, Devices: []device.ImportDevice{{DeviceID: one, VendorID: id, StationID: id, PortCount: 2, ProtocolAdapter: "untrusted", ReportsEnergy: true}}}
	key := "import_metadata_fault_" + rand.Text()
	if err := central.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "device_meta" {
			q.AddError(errors.New("central metadata unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, audit, scope, in); err == nil {
		t.Fatal("central fault accepted")
	}
	if err := central.Callback().Create().Remove(key); err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, q := range []struct {
		db    *gorm.DB /* 查询所属真库 */
		table string   /* 实际开通状态表 */
		want  int64    /* 中央未建、网关已建 */
	}{{central, "device_meta", 0}, {gateway, "device", 1}, {gateway, "device_port", 2}} {
		if err := q.db.Table(q.table).Where("device_id=?", one).Count(&n).Error; err != nil || n != q.want {
			t.Fatalf("%s count=%d want=%d %v", q.table, n, q.want, err)
		}
	}
	var job device.ImportJob
	if err := central.Table("device_import").Where("import_id=?", request).Take(&job).Error; err != nil || job.Status != "pending" {
		t.Fatalf("recovery task=%+v %v", job, err)
	}
	if out, err := service.Retry(ctx, audit, scope, request); err != nil || out.Status != "completed" {
		t.Fatalf("recover=%+v %v", out, err)
	}
	before := calls.Load()
	if out, err := service.Retry(ctx, audit, scope, request); err != nil || out.Status != "completed" || calls.Load() != before {
		t.Fatalf("completed replay=%+v %v calls=%d", out, err, calls.Load())
	}
	if err := central.Table("device_meta").Where("device_id=?", one).Update("model", "preserve-central-model").Error; err != nil {
		t.Fatal(err)
	}
	if out, err := service.Create(ctx, audit, scope, in); err != nil || out.Status != "completed" || calls.Load() != before {
		t.Fatalf("same input replay=%+v %v", out, err)
	}
	var model string
	if err := central.Table("device_meta").Where("device_id=?", one).Pluck("model", &model).Error; err != nil || model != "preserve-central-model" {
		t.Fatalf("config overwritten=%s %v", model, err)
	}
	changed := in
	changed.Devices = append([]device.ImportDevice(nil), in.Devices...)
	changed.Devices[0].StationID++
	if _, err := service.Create(ctx, audit, scope, changed); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("changed request=%v", err)
	}
	reject.Store(true)
	second := device.ImportInput{ImportID: secondRequest, Devices: []device.ImportDevice{{DeviceID: two, VendorID: id, StationID: id, PortCount: 2}}}
	out, err := service.Create(ctx, audit, scope, second)
	if err != nil || out.Status != "failed" || out.LastError == nil {
		t.Fatalf("failed task=%+v %v", out, err)
	}
	if err := central.Table("device_import").Where("import_id=?", secondRequest).Take(&job).Error; err != nil || job.Status != "failed" || job.Attempts != 1 {
		t.Fatalf("failure not committed=%+v %v", job, err)
	}
	reject.Store(false)
	if out, err := service.Retry(ctx, audit, scope, secondRequest); err != nil || out.Status != "completed" {
		t.Fatalf("failed retry=%+v %v", out, err)
	}
	for _, deviceID := range []string{one, two} {
		if err := gateway.Table("device").Where("device_id=?", deviceID).Count(&n).Error; err != nil || n != 1 {
			t.Fatalf("duplicate gateway device=%d %v", n, err)
		}
		if err := gateway.Table("device_port").Where("device_id=?", deviceID).Count(&n).Error; err != nil || n != 2 {
			t.Fatalf("duplicate ports=%d %v", n, err)
		}
	}
	if err := central.Table("audit_log").Where("request_id IN ?", []string{request, secondRequest}).Count(&n).Error; err != nil || n != 4 {
		t.Fatalf("audits=%d %v", n, err)
	}
}
