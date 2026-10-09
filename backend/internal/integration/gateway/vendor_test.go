package gateway

import (
	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/platform/serviceclient"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestVendorProxyForwardsConfigurationAndTrace 验证更新厂商时传递配置、服务令牌和追踪号；本地响应不验证数据库写入。
func TestVendorProxyForwardsConfigurationAndTrace(t *testing.T) {
	want := contract.VendorReply{ID: 7, VendorCode: "vendor_a", VendorName: "厂商 A", AdapterClass: "dc589", Protocol: "tcp", Status: "enabled"}
	input := contract.VendorRequest{VendorCode: "vendor_a", VendorName: "厂商 A", AdapterClass: "dc589", Protocol: "tcp", Status: "enabled"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/api/v1/internal/vendors/7" || r.Header.Get("X-Service-Token") != "test-vendor-token" || r.Header.Get("X-Request-ID") != "vendor-test" {
			t.Errorf("错误的代理请求：%s %s", r.Method, r.URL)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("请求格式或查询参数未保留")
		}
		var got contract.VendorRequest
		if json.NewDecoder(r.Body).Decode(&got) != nil || got != input {
			t.Errorf("配置未完整传递：%+v", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": want})
	}))
	defer server.Close()
	api := vendorTestClient(t, server.URL+"/", "test-vendor-token")
	got, err := api.UpdateVendor(context.Background(), 7, input, "vendor-test")
	if err != nil || got.ID != want.ID || got.VendorName != want.VendorName {
		t.Fatalf("代理结果错误：%+v %v", got, err)
	}
}

// TestVendorProxyFailsClosed 核对公开业务错误及私有错误、响应缺失和超限的安全映射。
func TestVendorProxyFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name, raw    string // 错误场景名称及响应正文
		status, want int    // 服务返回的状态和预期公开状态
	}{
		{"invalid", `{"code":1005,"message":"编码无效"}`, 400, 400},
		{"missing", `{"code":1004,"message":"厂商不存在"}`, 404, 404},
		{"duplicate", `{"code":2009,"message":"厂商编码已存在"}`, 409, 409},
		{"service_auth", `{"code":1001,"message":"private-token-error"}`, 401, 503},
		{"database", `{"code":5003,"message":"private-database-error"}`, 503, 503},
		{"malformed", `broken`, 200, 503},
		{"null", `{"code":0,"data":null}`, 200, 503},
		{"missing_code", `{"data":{}}`, 200, 503},
		{"empty_row", `{"code":0,"data":{}}`, 200, 503},
		{"oversize", strings.Repeat(" ", (1<<20)+1), 200, 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.raw))
			}))
			defer server.Close()
			api := vendorTestClient(t, server.URL, "test")
			_, err := api.Vendor(context.Background(), 1, "")
			var failure *contract.VendorFailure
			if !errors.As(err, &failure) || failure.Status != test.want {
				t.Fatalf("got %v, want HTTP %d", err, test.want)
			}
			if test.want == 503 && failure.Message != vendorUnavailable().Message {
				t.Fatalf("服务错误泄漏：%s", failure.Message)
			}
		})
	}
}

// TestVendorProxyDoesNotFollowRedirects 验证具名读取不会向重定向目标转发服务令牌。
func TestVendorProxyDoesNotFollowRedirects(t *testing.T) {
	forwarded := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	api := vendorTestClient(t, server.URL, "test")
	_, err := api.Vendor(context.Background(), 1, "")
	var failed *contract.VendorFailure
	if !errors.As(err, &failed) || failed.Status != 503 || forwarded {
		t.Fatalf("服务令牌不应被转发到重定向目标：forwarded=%v err=%v", forwarded, err)
	}
}

// TestVendorProxyHonorsCancellation 验证父 context 截止会终止读取，并保持统一依赖错误。
func TestVendorProxyHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	api := vendorTestClient(t, server.URL, "test")
	_, err := api.Vendor(ctx, 1, "")
	var failed *contract.VendorFailure
	if !errors.As(err, &failed) || failed.Status != 503 {
		t.Fatalf("取消请求应安全失败：%v", err)
	}
}

// vendorTestClient 为本地测试服务创建 gateway 客户端并注入令牌。
func vendorTestClient(t *testing.T, url, token string) *Client {
	t.Helper()
	client, err := NewClient(serviceclient.Client{}, url, token)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestVendorClientRejectsUnexpectedIdentity 核对读取与更新不能把请求的厂商编号替换为其他合法字典。
func TestVendorClientRejectsUnexpectedIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": contract.VendorReply{ID: 8, VendorCode: "other", VendorName: "其他厂商", AdapterClass: "dc589", Protocol: "tcp", Status: "enabled"}})
	}))
	defer server.Close()
	client := vendorTestClient(t, server.URL, "service")
	for _, update := range []bool{false, true} {
		var err error
		if update {
			_, err = client.UpdateVendor(context.Background(), 7, contract.VendorRequest{}, "request")
		} else {
			_, err = client.Vendor(context.Background(), 7, "request")
		}
		var failed *contract.VendorFailure
		if !errors.As(err, &failed) || failed.Status != 503 {
			t.Fatalf("unexpected identity update=%v err=%v", update, err)
		}
	}
}

// TestVendorListEscapesQueryAndKeepsPage 验证具名列表关键词转义、厂商集合及分页响应，不信任上游权限字段。
func TestVendorListEscapesQueryAndKeepsPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != "GET" || q.Get("keyword") != "厂商 A&+" || q.Get("ids") != "7,8" || q.Get("page") != "2" || q.Get("page_size") != "20" || q.Get("status") != "enabled" {
			t.Errorf("wrong list query: %s", r.URL)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []contract.VendorReply{}, "total": 3, "page": 2, "page_size": 20, "permissions": []string{"untrusted"}}})
	}))
	defer server.Close()
	client := vendorTestClient(t, server.URL, "service")
	out, err := client.Vendors(context.Background(), contract.VendorListRequest{Page: 2, PageSize: 20, Keyword: "厂商 A&+", Status: "enabled", IDs: []uint64{7, 8}}, "request")
	if err != nil || out.Total != 3 || out.Page != 2 || out.PageSize != 20 {
		t.Fatalf("list=%+v %v", out, err)
	}
}
