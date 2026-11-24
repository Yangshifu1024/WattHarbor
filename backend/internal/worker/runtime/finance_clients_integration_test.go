//go:build integration

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	contract "wattharbor/internal/contracts/central"
	centralhttp "wattharbor/internal/http/central"
	centralclient "wattharbor/internal/integration/central"
	"wattharbor/internal/platform/dbconn"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/platform/serviceclient"
	"wattharbor/internal/settlement"
	"wattharbor/internal/testfixture"
	"wattharbor/internal/worker/billing"
	"wattharbor/internal/worker/charge"
	"wattharbor/internal/worker/refund"
)

// financeOrderBoundary 仅固定订单入账边界及故障；真实 HTTP、资金循环和 fee_delivery SQL 执行，不扣真实资金。
type financeOrderBoundary struct {
	id      uint64       // 本 fixture 订单编号，不建立新订单或更换重试号。
	failing atomic.Bool  // 固定失败开关，恢复后仍使用回执。
	calls   atomic.Int64 // 订单 Apply 次数，验证到期前重扫和完成重放。
}

// Due 返回没有新增计算任务，保留本次投递恢复场景。
func (*financeOrderBoundary) Due(context.Context) ([]settlement.BillingTask, error) { return nil, nil }

// Read 拒绝新计算，防止恢复流程绕过持久费用载荷。
func (*financeOrderBoundary) Read(context.Context, uint64) (settlement.Source, error) {
	return settlement.Source{}, errors.New("unexpected new calculation")
}

// Apply 校验订单编号，记录调用并按开关返回固定依赖错误；不执行资金写入。
func (o *financeOrderBoundary) Apply(_ context.Context, result settlement.Result) error {
	if result.Source.ChargeOrderID != o.id {
		return errors.New("delivery changed original order identity")
	}
	o.calls.Add(1)
	if o.failing.Load() {
		return errors.New("original order apply unavailable")
	}
	return nil
}

// Defer 拒绝新计算任务重排，本 fixture 只能恢复 fee_delivery。
func (*financeOrderBoundary) Defer(context.Context, settlement.BillingTask, settlement.BillingJobStatus, string) error {
	return errors.New("unexpected new calculation defer")
}

// financeStopBoundary 提供停机进度与固定失败；可取消本轮受管理 context，不代表真实设备停机。
type financeStopBoundary struct {
	cancel context.CancelFunc // 仅取消测试持有的 worker 轮次；nil 保留普通部分失败。
}

// Run 返回 2 笔已处理和固定错误；设置 cancel 时先取消轮次，不创建 goroutine 或接触设备。
func (s financeStopBoundary) Run(context.Context) (int, error) {
	if s.cancel != nil {
		s.cancel()
	}
	return 2, errors.New("stop scan incomplete")
}

// financeBatchProbe 在 runtime 接口外记录专用调用方实际返回值，方法仍在调用 goroutine 执行。
type financeBatchProbe struct {
	next    BatchRunner // 实际 worker 自动停止调用方。
	count   int         // 本轮合法进度。
	failure error       // 调用方错误，不重建或吞掉分类。
}

// Run 原样调用 next 并记录其进度和错误，不添加重试或后台任务。
func (p *financeBatchProbe) Run(ctx context.Context) (int, error) {
	p.count, p.failure = p.next.Run(ctx)
	return p.count, p.failure
}

// financeDispatchProbe 记录是否进入派发方法，检查取消后不再调用下一项任务。
type financeDispatchProbe struct {
	next    Dispatcher // 实际 worker 计费或退款调用方。
	calls   int        // runtime 实际调用次数。
	failure error      // 调用结果。
}

// Run 记录入口再原样执行 next；不修改 context、调用顺序或失败语义。
func (p *financeDispatchProbe) Run(ctx context.Context) error {
	p.calls++
	p.failure = p.next.Run(ctx)
	return p.failure
}

// financeRequestLog 持有服务器实际收到的路径，以互斥保护 HTTP 线程和测试读取。
type financeRequestLog struct {
	mu    sync.Mutex // 保护 paths。
	paths []string   // 按到达顺序记录实际 POST 路径。
}

// snapshot 返回独立路径副本，不暴露服务器可变 slice。
func (l *financeRequestLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.paths...)
}

// registeredFinanceServer 挂载 central 真实三个任务 API，使用 BillingService 和限定自身的 MySQL 投递队列。
// 返回真实专用客户端、服务器及路径记录；调用方必须先关闭服务器再回滚事务及关闭数据库。
func registeredFinanceServer(t *testing.T, db *gorm.DB, orders *financeOrderBoundary, stopper financeStopBoundary) (*centralclient.Client, *httptest.Server, *financeRequestLog) {
	t.Helper()
	router := httpapi.NewRouter()
	const token = "runtime-registered-finance"
	centralhttp.AutoStopAPI{Stopper: stopper, ServiceToken: token}.Register(router)
	centralhttp.BillingAPI{Service: settlement.BillingService{Calculator: settlement.FeeCalculator{DB: db}, Orders: orders}, ServiceToken: token}.Register(router)
	centralhttp.RefundAPI{ServiceToken: token}.Register(router)
	requests := &financeRequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.mu.Lock()
		requests.paths = append(requests.paths, r.URL.Path)
		requests.mu.Unlock()
		router.ServeHTTP(w, r)
	}))
	client, err := centralclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, token)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return client, server, requests
}

// TestFinanceConsumersUseRegisteredJobsAndOriginalRecovery 验证真实注册 API、专用调用方与 financeOnce 顺序。
// 停机及计费失败仍进入退款；到期前不重投、更新同一身份后恢复、完成重放不重入账。
// 资金 Apply 与停机为测试替身，退款实际走禁用渠道；本测试覆盖任务次序和投递 SQL，不执行真实扣款或设备停机。
func TestFinanceConsumersUseRegisteredJobsAndOriginalRecovery(t *testing.T) {
	url := os.Getenv("TEST_MYSQL_URL")
	if url == "" {
		t.Fatal("TEST_MYSQL_URL required")
	}
	pool, err := dbconn.Open(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db, err := dbconn.WrapGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := testfixture.NewID()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	payload, err := json.Marshal(settlement.Result{Source: settlement.Source{ChargeOrderID: id, OrderNo: fmt.Sprintf("C-W-%d", id)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("fee_delivery").Create(map[string]any{"charge_order_id": id, "payload_json": string(payload), "scheduled_at": at}).Error; err != nil {
		t.Fatal(err)
	}
	scoped := tx.Scopes(func(q *gorm.DB) *gorm.DB {
		if q.Statement.Table == "fee_delivery" {
			return q.Where("charge_order_id=?", id)
		}
		return q
	})
	orders := &financeOrderBoundary{id: id}
	orders.failing.Store(true)
	client, server, requests := registeredFinanceServer(t, scoped, orders, financeStopBoundary{})
	defer server.Close()
	stops := &financeBatchProbe{next: charge.AutoStopper{Central: client}}
	bills := &financeDispatchProbe{next: billing.Dispatcher{Central: client}}
	refunds := &financeDispatchProbe{next: refund.Dispatcher{Central: client}}
	r := Runtime{AutoStops: stops, Billing: bills, Refunds: refunds}
	want := []string{contract.AutoStopPath, contract.BillingDispatchPath, contract.RefundDispatchPath}
	r.financeOnce(t.Context())
	var stopErr, billingErr *serviceclient.HTTPError
	if !reflect.DeepEqual(requests.snapshot(), want) || stops.count != 2 || !errors.As(stops.failure, &stopErr) || stopErr.StatusCode != 503 ||
		!errors.As(bills.failure, &billingErr) || billingErr.StatusCode != 503 || refunds.calls != 1 || refunds.failure != nil || orders.calls.Load() != 1 {
		t.Fatalf("original sequence=%v stopped=%d/%v billing=%v refund=%d/%v apply=%d", requests.snapshot(), stops.count, stops.failure, bills.failure, refunds.calls, refunds.failure, orders.calls.Load())
	}
	var state struct {
		Delivered bool      // 投递确认凭据。
		Attempts  int       // 失败尝试计数。
		Scheduled time.Time `gorm:"column:scheduled_at"` // 恢复时间。
	}
	if err := tx.Table("fee_delivery").Where("charge_order_id=?", id).Take(&state).Error; err != nil || state.Delivered || state.Attempts != 1 || !state.Scheduled.After(time.Now().UTC()) {
		t.Fatalf("original pending state=%+v err=%v", state, err)
	}
	r.financeOnce(t.Context())
	if bills.failure != nil || refunds.calls != 2 || orders.calls.Load() != 1 {
		t.Fatalf("before due billing=%v refund=%d apply=%d", bills.failure, refunds.calls, orders.calls.Load())
	}
	if err := tx.Table("fee_delivery").Where("charge_order_id=?", id).Update("scheduled_at", at).Error; err != nil {
		t.Fatal(err)
	}
	orders.failing.Store(false)
	r.financeOnce(t.Context())
	r.financeOnce(t.Context())
	if bills.failure != nil || refunds.calls != 4 || orders.calls.Load() != 2 {
		t.Fatalf("same identity recovery/replay billing=%v refund=%d apply=%d", bills.failure, refunds.calls, orders.calls.Load())
	}
	if err := tx.Table("fee_delivery").Where("charge_order_id=?", id).Take(&state).Error; err != nil || !state.Delivered || state.Attempts != 1 {
		t.Fatalf("original completed state=%+v err=%v", state, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	canceledClient, canceledServer, canceledRequests := registeredFinanceServer(t, scoped, orders, financeStopBoundary{cancel: cancel})
	defer canceledServer.Close()
	canceledBills := &financeDispatchProbe{next: billing.Dispatcher{Central: canceledClient}}
	canceledRefunds := &financeDispatchProbe{next: refund.Dispatcher{Central: canceledClient}}
	canceledRuntime := Runtime{AutoStops: charge.AutoStopper{Central: canceledClient}, Billing: canceledBills, Refunds: canceledRefunds}
	canceledRuntime.financeOnce(ctx)
	if canceledBills.calls != 0 || canceledRefunds.calls != 0 || !reflect.DeepEqual(canceledRequests.snapshot(), []string{contract.AutoStopPath}) || orders.calls.Load() != 2 {
		t.Fatalf("tasks entered after cancellation: requests=%v billing=%d refund=%d apply=%d", canceledRequests.snapshot(), canceledBills.calls, canceledRefunds.calls, orders.calls.Load())
	}
}
