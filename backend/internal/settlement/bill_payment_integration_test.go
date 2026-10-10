//go:build integration

package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"chargepilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// billReadOrders 用真实计价和出账链，只在资金入账边界计数；不将编排测试冒充真实扣款验收。
type billReadOrders struct {
	recoveryOrders
	source Source
	due    bool
}

func (o *billReadOrders) Due(context.Context) ([]BillingTask, error) {
	if o.due {
		o.due = false
		return []BillingTask{{ChargeOrderID: o.source.ChargeOrderID}}, nil
	}
	return nil, nil
}
func (o *billReadOrders) Read(context.Context, uint64) (Source, error) { return o.source, nil }

// TestPaymentReadFaultKeepsDeliveryCountAndMissingRecovery 分别覆盖新派发、已有投递、Missing 和同轮恢复。
// 故障仅注入 BillRepo 的支付读取；Calculate、MarkDelivered、Missing 与账单事件均为真实 SQL。
func TestPaymentReadFaultKeepsDeliveryCountAndMissingRecovery(t *testing.T) {
	for _, path := range []string{"due", "delivery", "missing", "same_round"} {
		t.Run(path, func(t *testing.T) {
			db, closeDB := testConn(t)
			defer closeDB()
			tx := db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			id := testfixture.NewID()
			no := "bill-run-" + uuid.NewString()
			at := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
			month := at.Format("2006-01") + "-01"
			source := feeSource(id, no)
			source.Meter.StartedAt = at
			source.Meter.EndedAt = at.Add(time.Minute)
			result := Result{Source: source, CalculationNo: CalculationNumber(no, id), ActualFee: *source.FinalFee}
			payload, _ := json.Marshal(result)
			for _, seed := range []struct {
				table string
				row   map[string]any
			}{
				{"charge_order", map[string]any{"id": id, "order_no": no, "user_id": source.UserID, "device_id": no, "port_no": 1, "status": "completed", "payment_order_id": id, "started_at": at, "created_month": month}},
				{"payment_order", map[string]any{"id": id, "order_no": "P-" + no, "user_id": source.UserID, "biz_type": "charge", "biz_id": id, "pay_method": "wechat", "total_cents": 1000, "paid_cents": 300, "refunded_cents": 50, "created_month": month}},
				{"charge_fee_receipt", map[string]any{"charge_order_id": id, "calculation_no": result.CalculationNo, "result_json": string(payload)}},
			} {
				if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
					t.Fatal(err)
				}
			}
			if path == "delivery" {
				if err := tx.Table("fee_delivery").Create(map[string]any{"charge_order_id": id, "payload_json": string(payload)}).Error; err != nil {
					t.Fatal(err)
				}
			}
			scoped := tx.Scopes(func(q *gorm.DB) *gorm.DB {
				if q.Statement.TableExpr != nil && q.Statement.TableExpr.SQL == "charge_fee_receipt f" {
					return q.Where("c.id=?", id)
				}
				if q.Statement.Table == "fee_delivery" {
					return q.Where("charge_order_id=?", id)
				}
				return q
			})
			orders := &billReadOrders{source: source, due: path == "due" || path == "same_round"}
			bills := BillRepo{DB: scoped}
			service := BillingService{Calculator: FeeCalculator{DB: scoped}, Orders: orders, Bills: bills}
			fault := errors.New("payment read fault")
			reads := 0
			callback := "test:bill-run-" + path
			if err := tx.Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
				if q.Statement.Table == "payment_order" {
					reads++
					if path != "same_round" || reads == 1 {
						q.AddError(fault)
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			n, err := service.Run(context.Background())
			tx.Callback().Query().Remove(callback)
			want := 1
			if path == "missing" {
				want = 0
			}
			if n != want || orders.applied != want || !errors.Is(err, fault) {
				t.Fatalf("first count=%d apply=%d err=%v", n, orders.applied, err)
			}
			var count int64
			if err := tx.Table("charge_bill").Where("charge_order_id=?", id).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			wantBills := int64(0)
			if path == "same_round" {
				wantBills = 1
			}
			if count != wantBills {
				t.Fatalf("first bills=%d want=%d", count, wantBills)
			}
			if n, err := service.Run(context.Background()); n != 0 || err != nil || orders.applied != want {
				t.Fatalf("recovery count=%d apply=%d err=%v", n, orders.applied, err)
			}
			bill, err := bills.Load(context.Background(), id)
			if err != nil || bill.PrepaidCents != 300 || bill.RefundCents != 50 || bill.ShortfallCents != 100 {
				t.Fatalf("recovered bill=%+v err=%v", bill, err)
			}
			for _, table := range []string{"charge_bill", "charge_event_log"} {
				if err := tx.Table(table).Where("charge_order_id=?", id).Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("%s count=%d err=%v", table, count, err)
				}
			}
		})
	}
}

// TestBillPaymentReadFailurePreservesRecovery 验证仅支付金额读取失败时账单和事件均不落库，缺支付同样失败。
// 解除故障后实付/退款/缺口正确，重放不重复出账；所有夹具由外层事务回滚。
func TestBillPaymentReadFailurePreservesRecovery(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := testfixture.NewID()
	no := "bill-pay-" + uuid.NewString()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	month := at.Format("2006-01") + "-01"
	source := feeSource(id, no)
	payload, _ := json.Marshal(Result{Source: source, CalculationNo: CalculationNumber(no, id), ActualFee: *source.FinalFee})
	for _, seed := range []struct {
		table string
		row   map[string]any
	}{
		{"charge_order", map[string]any{"id": id, "order_no": no, "user_id": source.UserID, "device_id": no, "port_no": 1, "status": "completed", "payment_order_id": id, "started_at": at, "created_month": month}},
		{"charge_fee_receipt", map[string]any{"charge_order_id": id, "calculation_no": CalculationNumber(no, id), "result_json": string(payload)}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(err)
		}
	}
	bills := BillRepo{DB: tx}
	ctx := context.Background()
	if _, _, err := bills.Issue(ctx, id); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing linked payment err=%v", err)
	}
	if err := tx.Table("payment_order").Create(map[string]any{"id": id, "order_no": "P-" + no, "user_id": source.UserID, "biz_type": "charge", "biz_id": id, "pay_method": "wechat", "total_cents": 1000, "paid_cents": 300, "refunded_cents": 50, "created_month": month}).Error; err != nil {
		t.Fatal(err)
	}
	fault := errors.New("payment amount query fault")
	const callback = "test:bill-payment-read-fault"
	if err := tx.Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
		if q.Statement.Table == "payment_order" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Callback().Query().Remove(callback) })
	if _, _, err := bills.Issue(ctx, id); !errors.Is(err, fault) {
		t.Fatalf("injected query err=%v", err)
	}
	for _, table := range []string{"charge_bill", "charge_event_log"} {
		var n int64
		if err := tx.Table(table).Where("charge_order_id=?", id).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("%s count=%d err=%v", table, n, err)
		}
	}
	tx.Callback().Query().Remove(callback)
	for range 2 {
		bill, ok, err := bills.Issue(ctx, id)
		if err != nil || !ok || bill.PrepaidCents != 300 || bill.RefundCents != 50 || bill.ShortfallCents != 100 {
			t.Fatalf("bill=%+v ok=%v err=%v", bill, ok, err)
		}
	}
	for _, table := range []string{"charge_bill", "charge_event_log"} {
		var n int64
		if err := tx.Table(table).Where("charge_order_id=?", id).Count(&n).Error; err != nil || n != 1 {
			t.Fatalf("%s count=%d err=%v", table, n, err)
		}
	}
}
