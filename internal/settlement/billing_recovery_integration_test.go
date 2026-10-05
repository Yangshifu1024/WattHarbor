//go:build integration

package settlement

import (
	"github.com/ChargePilot2026/charge-pilot/internal/testfixture"

	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"testing"
	"time"
)

// recoveryOrders 只记录投递调用；真实资金入账由既有 BillingOrders 并发测试覆盖。
type recoveryOrders struct {
	applied int // 已尝试投递原费用的次数，补账单不得再次推进
}

// Due 返回无新任务、无错误；测试只允许恢复已有投递，不访问数据库。
func (o *recoveryOrders) Due(context.Context) ([]uint64, error) { return nil, nil }

// Read 对任一 context 下的订单读取报错，确保补账单未重新计算费用。
func (o *recoveryOrders) Read(context.Context, uint64) (Source, error) {
	return Source{}, errors.New("unexpected calculation")
}

// Apply 接收原 Result，增加投递计数并返回 nil；不模拟实际资金写入。
func (o *recoveryOrders) Apply(context.Context, Result) error { o.applied++; return nil }

// Defer 对任一 context、订单、状态及原因报错；本场景不得重排计算任务。
func (o *recoveryOrders) Defer(context.Context, uint64, string, string) error {
	return errors.New("unexpected defer")
}

// failingBillIssuer 仅在出账边界注入故障，查询、唯一键和恢复写入仍走真库。
type failingBillIssuer struct {
	BillRepo      // 实际账单读取、唯一键及补出账实现
	fail     bool // 是否在出账边界返回故障
}

// Issue 用 ctx 为订单 id 申请实际账单，返回账单、符合出账条件标志及错误；故障时不创建账单。
func (b *failingBillIssuer) Issue(ctx context.Context, id uint64) (Bill, bool, error) {
	if b.fail {
		return Bill{}, false, errors.New("bill IO boundary unavailable")
	}
	return b.BillRepo.Issue(ctx, id)
}

// TestDeliveredFeeCanRecoverMissingBill 验证 backlog 确认后出账失败不会丢失恢复入口。
// 第一次只标记原投递，下一次补账单不重新 Apply；全部数据使用外层 tx 回滚。
func TestDeliveredFeeCanRecoverMissingBill(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := testfixture.NewID()
	no := "C-BILL-" + rand.Text()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	source := feeSource(id, no)
	source.Meter.EndedAt = at
	source.Meter.StartedAt = at.Add(-time.Hour)
	result := Result{Source: source, CalculationNo: CalculationNumber(no, id), ActualFee: *source.FinalFee}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct {
		table string         // 原订单、费用及待投递所属表
		row   map[string]any // 同一原结算身份的必需事实
	}{
		{"charge_order", map[string]any{"id": id, "order_no": no, "user_id": source.UserID, "device_id": no, "port_no": 1, "status": "completed", "payment_status": "paid", "started_at": source.Meter.StartedAt, "ended_at": at, "created_month": at.Format("2006-01") + "-01"}},
		{"charge_fee_receipt", map[string]any{"charge_order_id": id, "calculation_no": result.CalculationNo, "result_json": string(payload), "shortfall_cents": 0}},
		{"fee_delivery", map[string]any{"charge_order_id": id, "payload_json": string(payload)}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 隔离本测试的扫描，避免并行包中的临时业务行进入 backlog 或恢复清单。
	scoped := tx.Scopes(func(q *gorm.DB) *gorm.DB {
		if q.Statement.TableExpr != nil && q.Statement.TableExpr.SQL == "charge_fee_receipt f" {
			return q.Where("c.id=?", id)
		}
		if q.Statement.Table == "fee_delivery" {
			return q.Where("charge_order_id=?", id)
		}
		return q
	})
	orders := &recoveryOrders{}
	bills := &failingBillIssuer{BillRepo: BillRepo{DB: scoped}, fail: true}
	s := BillingService{Calculator: FeeCalculator{DB: scoped}, Orders: orders, Bills: bills}
	if n, err := s.Run(context.Background()); n != 1 || err == nil || orders.applied != 1 {
		t.Fatalf("first n=%d err=%v apply=%d", n, err, orders.applied)
	}
	var delivered bool
	if err := tx.Table("fee_delivery").Where("charge_order_id=?", id).Pluck("delivered", &delivered).Error; err != nil || !delivered {
		t.Fatalf("delivered=%v err=%v", delivered, err)
	}
	bills.fail = false
	if n, err := s.Run(context.Background()); n != 0 || err != nil || orders.applied != 1 {
		t.Fatalf("recover n=%d err=%v apply=%d", n, err, orders.applied)
	}
	for i := 0; i < 2; i++ {
		bill, _, err := bills.Issue(context.Background(), id)
		if err != nil || bill.TotalCents != 400 {
			t.Fatalf("bill=%+v err=%v", bill, err)
		}
	}
	for _, table := range []string{"charge_bill", "charge_fee_receipt", "charge_event_log"} {
		var count int64
		if err := tx.Table(table).Where("charge_order_id=?", id).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}
