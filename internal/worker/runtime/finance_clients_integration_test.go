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

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	centralhttp "github.com/ChargePilot2026/charge-pilot/internal/http/central"
	centralclient "github.com/ChargePilot2026/charge-pilot/internal/integration/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/refund"
	"gorm.io/gorm"
)

// financeOrderBoundary 仅固定原订单入账边界及故障；真实HTTP、资金循环和fee_delivery SQL执行，不扣真实资金。
type financeOrderBoundary struct {
	id      uint64       // 本fixture原订单身份，不建立新订单或更换重试号。
	failing atomic.Bool  // 固定失败开关，恢复后仍使用原回执。
	calls   atomic.Int64 // 原订单Apply次数，验证到期前重扫和完成重放。
}

// Due 返回没有新增计算任务，保留本次原投递恢复场景；无IO。
func (*financeOrderBoundary) Due(context.Context) ([]uint64, error) { return nil, nil }

// Read 拒绝新计算，防止恢复流程绕过原持久费用载荷；无IO。
func (*financeOrderBoundary) Read(context.Context, uint64) (settlement.Source, error) {
	return settlement.Source{}, errors.New("unexpected new calculation")
}

// Apply 校验原订单身份，记录调用并按开关返回固定依赖错误；不执行资金写入。
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

// Defer 拒绝新计算任务重排，本fixture只能恢复原fee_delivery；无IO。
func (*financeOrderBoundary) Defer(context.Context, uint64, string, string) error {
	return errors.New("unexpected new calculation defer")
}

// financeStopBoundary 提供原停机进度与固定失败；可取消本轮受管理context，不代表真实设备停机。
type financeStopBoundary struct {
	cancel context.CancelFunc // 仅取消测试持有的worker轮次；nil保留普通部分失败。
}

// Run 返回2笔已处理和固定错误；设置cancel时先取消轮次，不创建goroutine或接触设备。
func (s financeStopBoundary) Run(context.Context) (int, error) {
	if s.cancel != nil {
		s.cancel()
	}
	return 2, errors.New("stop scan incomplete")
}

// financeBatchProbe 在原runtime接口外记录具名消费者实际返回值，方法仍在原调用goroutine执行。
type financeBatchProbe struct {
	next    BatchRunner // 实际worker自动停止消费者。
	count   int         // 本轮原合法进度。
	failure error       // 原消费者错误，不重建或吞掉分类。
}

// Run 原样调用next并记录其进度和错误，不添加重试或后台任务。
func (p *financeBatchProbe) Run(ctx context.Context) (int, error) {
	p.count, p.failure = p.next.Run(ctx)
	return p.count, p.failure
}

// financeDispatchProbe 记录runtime是否进入原派发消费者，取消后不能以HTTP未发出替代入口未调用的证明。
type financeDispatchProbe struct {
	next    Dispatcher // 实际worker计费或退款消费者。
	calls   int        // runtime实际调用次数。
	failure error      // 原调用结果。
}

// Run 记录入口再原样执行next；不修改context、调用顺序或失败语义。
func (p *financeDispatchProbe) Run(ctx context.Context) error {
	p.calls++
	p.failure = p.next.Run(ctx)
	return p.failure
}

// financeRequestLog 持有服务器实际收到的路径，以互斥保护HTTP线程和测试读取。
type financeRequestLog struct {
	mu    sync.Mutex // 保护paths。
	paths []string   // 按到达顺序记录实际POST路径。
}

// snapshot 返回独立路径副本，不暴露服务器可变slice；无外部IO。
func (l *financeRequestLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.paths...)
}

// registeredFinanceServer 挂载central真实三个任务API，使用原BillingService和限定自身的MySQL投递队列。
// 返回真实具名客户端、服务器及路径记录；调用方必须先关闭服务器再回滚事务及关闭数据库。
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

// TestFinanceConsumersUseRegisteredJobsAndOriginalRecovery 验证真实注册API、具名消费者与原financeOnce顺序。
// 停机及计费失败仍进入退款；原到期前不重投、推进同一身份后恢复、完成重放不重入账。
// 资金Apply与停机为窄边界fixture，退款实际走禁用渠道；只证明编排和投递SQL，不替代资金或实机验收。
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
	id := uint64(time.Now().UnixNano())
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
		Delivered bool      // 原投递确认凭据。
		Attempts  int       // 原失败尝试计数。
		Scheduled time.Time `gorm:"column:scheduled_at"` // 原恢复时间。
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
