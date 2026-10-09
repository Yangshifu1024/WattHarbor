package device_test

import (
	"chargepilot/internal/device"
	"chargepilot/internal/domain/operations"
	gatewayclient "chargepilot/internal/integration/gateway"
	"chargepilot/internal/persist"
	"chargepilot/internal/platform/serviceclient"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestVendorQueryUsesAccountScope 核对只有账号范围进入查询，站点限制不会冒充厂商限制。
func TestVendorQueryUsesAccountScope(t *testing.T) {
	for _, scope := range []operations.DataScope{{Unrestricted: true, VendorIDs: []uint64{3}}, {StationIDs: []uint64{5}}, {VendorIDs: []uint64{3, 7}}} {
		q := device.VendorQuery(persist.PageQuery{Page: 2, PageSize: 20, Keyword: "厂商", Status: "enabled"}, scope)
		want := ""
		if !scope.Unrestricted && len(scope.VendorIDs) > 0 {
			want = "3,7"
		}
		if strings.Join(func() []string {
			out := []string{}
			for _, id := range q.IDs {
				out = append(out, strconv.FormatUint(id, 10))
			}
			return out
		}(), ",") != want || q.Status != "enabled" || q.Page != 2 {
			t.Fatalf("范围查询错误：%+v", q)
		}
		if scope.AllowsVendor(9) != (want == "") || !scope.AllowsVendor(3) {
			t.Fatalf("详情范围与列表不一致：%+v", scope)
		}
	}
}

// TestVendorScopeValidationUsesGateway 使用 loopback 字典总数验证范围存在性与零 ID 拒绝，不验证真实厂商表。
func TestVendorScopeValidationUsesGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ids") != "3,7" || r.URL.Query().Get("page_size") != "1" {
			t.Error("范围未通过网关验证")
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[],"total":2,"page":1,"page_size":1}}`))
	}))
	defer server.Close()
	client, err := gatewayclient.NewClient(serviceclient.Client{}, server.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	api := device.VendorService{Gateway: client}
	if err := api.ValidateScope(context.Background(), "vendor", []uint64{3, 7}); err != nil {
		t.Fatal(err)
	}
	if err := api.ValidateScope(context.Background(), "vendor", []uint64{0}); !errors.Is(err, device.ErrScopeConflict) {
		t.Fatalf("零 ID 应拒绝：%v", err)
	}
}
