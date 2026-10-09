package central

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	chargestart "chargepilot/internal/charging/start"
	centralclient "chargepilot/internal/integration/central"
	"chargepilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// paidStartsStub 记录游标和调用次数，返回固定待启动订单或指定错误。
type paidStartsStub struct {
	after uint64 // 最近一次请求的 after_id 游标。
	calls int    // PaidStarts 调用次数，用于核对拒绝请求未访问用例。
	err   error  // 指定的读取错误，原样交给 HTTP 映射，不执行数据库操作。
}

// TestPaidStartsClientMatchesRegisteredCentralRoute 核对专用客户端与真实注册路由的游标及公开集合。
// 用例为固定 fixture，覆盖一条记录和空数组，不验证真实库筛选、并发或派发设备命令。
func TestPaidStartsClientMatchesRegisteredCentralRoute(t *testing.T) {
	service := &paidStartsStub{}
	router := gin.New()
	PaidStartsAPI{Service: service, ServiceToken: "secret"}.Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := centralclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := client.PaidStarts(context.Background(), 9)
	if err != nil || len(rows) != 1 || rows[0].ID != 10 || rows[0].OrderNo != "C-10" || service.after != 9 {
		t.Fatalf("rows=%v cursor=%d err=%v", rows, service.after, err)
	}
	rows, err = client.PaidStarts(context.Background(), 10)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty scan: rows=%v err=%v", rows, err)
	}
}

// PaidStarts 记录游标；游标为 9 时返回固定订单，其余返回空集合，所有分支均返回预设错误。
func (s *paidStartsStub) PaidStarts(_ context.Context, after uint64) ([]chargestart.PaidChargeOrder, error) {
	s.after, s.calls = after, s.calls+1
	if after == 9 {
		return []chargestart.PaidChargeOrder{{ID: 10, OrderNo: "C-10"}}, s.err
	}
	return nil, s.err
}

// TestPaidStartsHTTPContract 检查真实 GET 路由的令牌、游标、数组、存储错误脱敏及用例调用次数。
// 用例为测试替身，验证 400／401／503 映射及拒绝后不调用，不代表真实 MySQL 或设备执行证据。
func TestPaidStartsHTTPContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		query, token  string // 游标查询串和提供的服务令牌。
		err           error  // fixture 返回的读取错误，nil 表示读取成功。
		status, calls int    // 预期 HTTP 状态及查询调用次数。
		body          string // 响应必须包含的字段或提示。
	}{
		{"", "", nil, 401, 0, "service token invalid"},
		{"?after_id=-1", "secret", nil, 400, 0, "invalid paid start cursor"},
		{"?after_id=18446744073709551616", "secret", nil, 400, 0, "invalid paid start cursor"},
		{"?after_id=9", "secret", nil, 200, 1, `"items":[{"id":10,"order_no":"C-10"}]`},
		{"", "secret", nil, 200, 1, `"items":[]`},
		{"", "secret", errors.New("private SQL error"), 503, 1, "paid orders unavailable"},
	} {
		s := &paidStartsStub{err: test.err}
		r := gin.New()
		PaidStartsAPI{Service: s, ServiceToken: "secret"}.Register(r)
		request := httptest.NewRequest("GET", "/api/v1/internal/charge-orders/paid-starts"+test.query, nil)
		request.Header.Set("X-Service-Token", test.token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, request)
		if w.Code != test.status || s.calls != test.calls || !strings.Contains(w.Body.String(), test.body) || strings.Contains(w.Body.String(), "private SQL error") {
			t.Fatalf("query=%s status=%d calls=%d body=%s", test.query, w.Code, s.calls, w.Body.String())
		}
		if test.query == "?after_id=9" && s.after != 9 {
			t.Fatalf("cursor=%d", s.after)
		}
	}
}
