//go:build integration

package provision

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	contract "chargepilot/internal/contracts/gateway"
	gatewayrepo "chargepilot/internal/gateway/repo"
	client "chargepilot/internal/integration/gateway"
	"chargepilot/internal/platform/serviceclient"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestVendorOperationsKeepConstraintsAndRollback 经实际注册路由使用专用客户端核对完整厂商操作。
// 使用独立网关真库，只创建私有厂商／设备；取消发生在更新 SQL 完成后，必须回滚同一事务的全部列。
func TestVendorOperationsKeepConstraintsAndRollback(t *testing.T) {
	db, original, devices := provisionFixture(t)
	router := vendorTestRouter(API{DB: db, ServiceToken: vendorTestToken})
	server := httptest.NewServer(router)
	defer server.Close()
	gateway, err := client.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, vendorTestToken)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	code := "VF" + strings.ReplaceAll(uuid.NewString(), "-", "")
	// 预登记未知提交结果的待清理记录的编号，实际账号等其它表不参与本测试。
	t.Cleanup(func() {
		var ids []uint64
		if err := db.Table("vendor").Where("vendor_code=?", code).Pluck("id", &ids).Error; err != nil {
			t.Error(err)
			return
		}
		for _, id := range ids {
			if err := db.Table("vendor").Where("id=?", id).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
		}
		var n int64
		if err := db.Table("vendor").Where("vendor_code=?", code).Count(&n).Error; err != nil || n != 0 {
			t.Errorf("vendor fixture remaining=%d err=%v", n, err)
		}
	})
	in := contract.VendorRequest{VendorCode: code, VendorName: "  私有%_!厂商  ", AdapterClass: "dc589", Protocol: "tcp", Status: "disabled"}
	created, err := gateway.CreateVendor(ctx, in, "vendor-create")
	if err != nil || created.ID == 0 || created.VendorName != "私有%_!厂商" || created.EnabledAt != nil || created.Status != "disabled" || created.CreatedAt.Nanosecond()%int(time.Millisecond) != 0 || !created.CreatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("create reply=%+v err=%v", created, err)
	}
	wantFailure := func(err error, status, code int, message string) {
		t.Helper()
		var failure *contract.VendorFailure
		if !errors.As(err, &failure) || failure.Status != status || failure.Code != code || failure.Message != message {
			t.Fatalf("failure=%v want=%d/%d/%s", err, status, code, message)
		}
	}
	_, err = gateway.CreateVendor(ctx, in, "vendor-duplicate")
	wantFailure(err, 409, 2009, "厂商编码已存在")
	in.Status = "enabled"
	enabled, err := gateway.UpdateVendor(ctx, created.ID, in, "vendor-enable")
	if err != nil || enabled.EnabledAt == nil || !enabled.EnabledAt.Equal(enabled.UpdatedAt) || !enabled.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("enable reply=%+v err=%v", enabled, err)
	}
	for _, status := range []string{"enabled", "disabled"} {
		in.Status = status
		row, err := gateway.UpdateVendor(ctx, created.ID, in, "vendor-state")
		if err != nil || row.Status != status || row.EnabledAt == nil || !row.EnabledAt.Equal(*enabled.EnabledAt) || !row.CreatedAt.Equal(created.CreatedAt) {
			t.Fatalf("state=%s reply=%+v err=%v", status, row, err)
		}
	}
	in.Status = "enabled"
	row, err := gateway.UpdateVendor(ctx, created.ID, in, "vendor-reenable")
	if err != nil || row.EnabledAt == nil || !row.EnabledAt.Equal(row.UpdatedAt) {
		t.Fatalf("reenable reply=%+v err=%v", row, err)
	}
	if err := db.Model(&gatewayrepo.Vendor{}).Where("id=?", original.ID).Update("vendor_name", "私有ABC厂商").Error; err != nil {
		t.Fatal(err)
	}
	for _, keyword := range []string{"%", "_", "!"} {
		list, err := gateway.Vendors(ctx, contract.VendorListRequest{Page: 1, PageSize: 20, Keyword: keyword, Status: "enabled", IDs: []uint64{original.ID, created.ID}}, "vendor-list")
		if err != nil || list.Total != 1 || len(list.Items) != 1 || list.Items[0].ID != created.ID || list.Page != 1 || list.PageSize != 20 {
			t.Fatalf("literal keyword=%q list=%+v err=%v", keyword, list, err)
		}
	}
	read, err := gateway.Vendor(ctx, created.ID, "vendor-get")
	if err != nil || !reflect.DeepEqual(read, row) {
		t.Fatalf("get reply=%+v err=%v", read, err)
	}
	empty, err := gateway.Vendors(ctx, contract.VendorListRequest{Page: 1, PageSize: 20, Status: "disabled", IDs: []uint64{created.ID}}, "vendor-empty")
	if err != nil || empty.Total != 0 || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("empty list must retain []: reply=%+v err=%v", empty, err)
	}
	changed := in
	changed.VendorCode += "other"
	_, err = gateway.UpdateVendor(ctx, created.ID, changed, "vendor-code-conflict")
	wantFailure(err, 409, 2009, "厂商编码创建后不可修改")
	if err := gateway.Provision(ctx, devices[:1]); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Table("device").Where("device_id=?", devices[0].DeviceID).Updates(map[string]any{"status": "retired", "deleted_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	changed = contract.VendorRequest{VendorCode: original.VendorCode, VendorName: "拒绝写入", AdapterClass: "other", Protocol: "mqtt", Status: "enabled"}
	_, err = gateway.UpdateVendor(ctx, original.ID, changed, "vendor-device-conflict")
	wantFailure(err, 409, 2009, "厂商已有设备，适配器和连接协议不可修改")
	if err := db.Table("device").Where("device_id=?", devices[0].DeviceID).Delete(map[string]any{}).Error; err != nil {
		t.Fatal(err)
	}
	_, err = gateway.UpdateVendor(ctx, original.ID, changed, "vendor-unsupported")
	wantFailure(err, 400, 1005, "当前网关仅支持 dc589 适配器与 TCP 连接")
	if err := db.Table("vendor").Where("id=?", created.ID).Update("deleted_at", now).Error; err != nil {
		t.Fatal(err)
	}
	_, err = gateway.Vendor(ctx, created.ID, "vendor-missing")
	wantFailure(err, 404, 1004, "厂商不存在")
	if err := db.Table("vendor").Where("id=?", created.ID).Update("deleted_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	var before gatewayrepo.Vendor
	if err := db.Where("id=?", created.ID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	var once sync.Once
	const hook = "proof:vendor_update_cancel"
	if err := db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register(hook, func(tx *gorm.DB) {
		if tx.Error == nil && tx.RowsAffected == 1 && tx.Statement.Table == "vendor" && strings.HasPrefix(tx.Statement.SQL.String(), "UPDATE") {
			once.Do(cancel)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Update().Remove(hook)
	in.VendorName, in.Status = "取消后不得保留", "disabled"
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, contract.VendorPath+"/"+strconv.FormatUint(created.ID, 10), strings.NewReader(string(body))).WithContext(canceled)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Token", vendorTestToken)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if canceled.Err() != context.Canceled || response.Code != 503 || !strings.Contains(response.Body.String(), `"code":5003`) || !strings.Contains(response.Body.String(), `"data":null`) || !strings.Contains(response.Body.String(), "厂商管理暂时不可用") {
		t.Fatalf("cancel err=%v response=%d/%s", canceled.Err(), response.Code, response.Body.String())
	}
	if err := db.Callback().Update().Remove(hook); err != nil {
		t.Fatal(err)
	}
	var after gatewayrepo.Vendor
	if err := db.Where("id=?", created.ID).Take(&after).Error; err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("vendor partial write before=%+v after=%+v err=%v", before, after, err)
	}
}
