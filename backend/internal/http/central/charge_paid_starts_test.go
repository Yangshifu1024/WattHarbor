package central

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	chargestart "github.com/ChargePilot2026/charge-pilot/internal/charging/start"
	centralclient "github.com/ChargePilot2026/charge-pilot/internal/integration/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// paidStartsStub 记录HTTP传入的原游标及调用次数，返回固定待启动事实或指定读取错误。
type paidStartsStub struct {
	after uint64 // 最近一次请求的原after_id游标。
	calls int    // PaidStarts调用次数，用于核对拒绝请求未访问用例。
	err   error  // 指定的读取错误，原样交给HTTP映射，不执行数据库操作。
}

// TestPaidStartsClientMatchesRegisteredCentralRoute 用t核对具名客户端与真实注册路由的游标及公开集合。
// 用例为固定fixture，覆盖一条记录和空数组，不验证真实库筛选、并发或派发设备命令。
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

// PaidStarts 忽略输入context，以after更新s记录和次数；游标9返回固定订单，其余返回nil集合。
// 所有分支返回s.err，无IO、后台任务或取消模拟；空集合的公开数组形状由真实HTTP适配及客户端核对。
func (s *paidStartsStub) PaidStarts(_ context.Context, after uint64) ([]chargestart.PaidChargeOrder, error) {
	s.after, s.calls = after, s.calls+1
	if after == 9 {
		return []chargestart.PaidChargeOrder{{ID: 10, OrderNo: "C-10"}}, s.err
	}
	return nil, s.err
}

// TestPaidStartsHTTPContract 用t检查真实GET路由的令牌、游标、数组、存储错误脱敏及用例调用次数。
// 用例为窄fixture，验证400／401／503映射及拒绝后不调用，不代表真实MySQL或设备执行证据。
func TestPaidStartsHTTPContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		query, token  string // 原游标查询串和提供的服务令牌。
		err           error  // fixture返回的原读取错误，nil表示读取成功。
		status, calls int    // 预期HTTP状态及所属查询调用次数。
		body          string // 响应必须包含的原公开形状或提示片段。
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
