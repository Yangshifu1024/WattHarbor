//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	contract "chargepilot/internal/contracts/central"
	centralhttp "chargepilot/internal/http/central"
	centralclient "chargepilot/internal/integration/central"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/platform/serviceclient"
	"chargepilot/internal/settlement"
	"chargepilot/internal/testfixture"
	"gorm.io/gorm"
)

// financeDeliveryOrders 只在订单入账边界记录标识并固定第二笔故障。
// HTTP、BillingService、计费投递 SQL 均真实执行；此 fixture 不扣款或模拟真实资金幂等。
type financeDeliveryOrders struct {
	failing uint64         // 当前固定失败的订单主键，零表示恢复依赖。
	calls   map[uint64]int // 每个身份的 Apply 次数，核对重放没有换主键或重投已确认笔。
}

// Due 返回无新计算任务及 nil 错误，只允许恢复投递；不访问数据库。
func (o *financeDeliveryOrders) Due(context.Context) ([]uint64, error) { return nil, nil }

// Read 对任何 ctx 和 id 返回错误，确保恢复投递没有重新计算。
func (o *financeDeliveryOrders) Read(context.Context, uint64) (settlement.Source, error) {
	return settlement.Source{}, errors.New("unexpected calculation")
}

// Apply 记录 result 订单主键，对 failing 返回固定错误，其余成功；只修改 fixture 计数。
func (o *financeDeliveryOrders) Apply(_ context.Context, result settlement.Result) error {
	id := result.Source.ChargeOrderID
	o.calls[id]++
	if id == o.failing {
		return errors.New("fixed order apply boundary unavailable")
	}
	return nil
}

// Defer 对任何 ctx、id、状态及原因返回错误，本场景不得进入新计算任务重排。
func (o *financeDeliveryOrders) Defer(context.Context, uint64, string, string) error {
	return errors.New("unexpected new calculation defer")
}

// TestRegisteredBillingClientKeepsPartialProgressAndOriginalDelivery 验证真实注册路由和 MySQL 投递恢复。
// 固定资金边界故障只证明回复及队列身份，不作为实际扣款验收；造数及更新均外层回滚。
func TestRegisteredBillingClientKeepsPartialProgressAndOriginalDelivery(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	first := testfixture.NewID()
	second := first + 1
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	for _, id := range []uint64{first, second} {
		payload, err := json.Marshal(settlement.Result{Source: settlement.Source{ChargeOrderID: id, OrderNo: fmt.Sprintf("C-X-%d", id)}})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Table("fee_delivery").Create(map[string]any{"charge_order_id": id, "payload_json": string(payload), "scheduled_at": at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 只约束本次队列扫描，避免其它并行包的临时回执被此 fixture 处理。
	scoped := tx.Scopes(func(q *gorm.DB) *gorm.DB {
		if q.Statement.Table == "fee_delivery" {
			return q.Where("charge_order_id IN ?", []uint64{first, second})
		}
		return q
	})
	orders := &financeDeliveryOrders{failing: second, calls: map[uint64]int{}}
	router := httpapi.NewRouter()
	centralhttp.BillingAPI{Service: settlement.BillingService{Calculator: settlement.FeeCalculator{DB: scoped}, Orders: orders}, ServiceToken: "registered-finance"}.Register(router)
	server := httptest.NewServer(router)
	defer server.Close()
	client, err := centralclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "registered-finance")
	if err != nil {
		t.Fatal(err)
	}
	bad, err := centralclient.NewClient(serviceclient.Client{HTTP: server.Client()}, server.URL, "wrong-finance")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.DispatchBilling(context.Background(), contract.BillingDispatchRequest{}); !errors.Is(err, serviceclient.ErrUnauthorized) || len(orders.calls) != 0 {
		t.Fatalf("bad identity entered business: err=%v calls=%v", err, orders.calls)
	}
	reply, err := client.DispatchBilling(context.Background(), contract.BillingDispatchRequest{})
	var failure *serviceclient.HTTPError
	if reply.Completed != 1 || !errors.As(err, &failure) || failure.StatusCode != 503 || orders.calls[first] != 1 || orders.calls[second] != 1 {
		t.Fatalf("partial progress=%+v err=%v calls=%v", reply, err, orders.calls)
	}
	var states []struct {
		ID        uint64    `gorm:"column:charge_order_id"` // 投递标识，不换成重试业务号。
		Delivered bool      `gorm:"column:delivered"`       // 完成凭据，已完成笔不重投。
		Attempts  int       `gorm:"column:attempts"`        // 失败尝试计数。
		Scheduled time.Time `gorm:"column:scheduled_at"`    // 失败后的持久重试时间。
	}
	if err := tx.Table("fee_delivery").Where("charge_order_id IN ?", []uint64{first, second}).Order("charge_order_id").Find(&states).Error; err != nil || len(states) != 2 {
		t.Fatalf("states=%+v err=%v", states, err)
	}
	if !states[0].Delivered || states[0].Attempts != 0 || states[1].Delivered || states[1].Attempts != 1 || !states[1].Scheduled.After(time.Now().UTC()) {
		t.Fatalf("original recovery state=%+v", states)
	}
	if reply, err := client.DispatchBilling(context.Background(), contract.BillingDispatchRequest{}); err != nil || reply.Completed != 0 || orders.calls[first] != 1 || orders.calls[second] != 1 {
		t.Fatalf("retry before due=%+v err=%v calls=%v", reply, err, orders.calls)
	}
	// 测试更新同一任务的到期时间，避免等待真实一分钟；不重建回执或更换订单编号。
	if err := tx.Table("fee_delivery").Where("charge_order_id=?", second).Update("scheduled_at", at).Error; err != nil {
		t.Fatal(err)
	}
	orders.failing = 0
	if reply, err := client.DispatchBilling(context.Background(), contract.BillingDispatchRequest{}); err != nil || reply.Completed != 1 || orders.calls[first] != 1 || orders.calls[second] != 2 {
		t.Fatalf("original due recovery=%+v err=%v calls=%v", reply, err, orders.calls)
	}
	if reply, err := client.DispatchBilling(context.Background(), contract.BillingDispatchRequest{}); err != nil || reply.Completed != 0 || orders.calls[first] != 1 || orders.calls[second] != 2 {
		t.Fatalf("replayed completed delivery=%+v err=%v calls=%v", reply, err, orders.calls)
	}
}
