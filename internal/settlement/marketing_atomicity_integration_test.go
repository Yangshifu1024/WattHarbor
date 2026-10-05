//go:build integration

package settlement

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"testing"
	"time"
)

// TestBillingMarketingStorageFailureRollsBackFunds 验证营销读取故障返回原资金事务并回滚已写费用，而正常不适用仍可完成原结算。
func TestBillingMarketingStorageFailureRollsBackFunds(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	defer tx.Rollback()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	id := testfixture.NewID()
	no := "C-BR-" + rand.Text()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	rule := charging.Rule{ID: 1, StationID: 1, Version: 1, Spec: charging.Spec{Scheme: &charging.Scheme{}}}
	contract, _ := json.Marshal(map[string]any{"rule": rule, "offer": charging.Offer{ID: 1, PriceCents: 10000}})
	meter, _ := json.Marshal(charging.EndMeter{EndedAt: at, ChargedWh: 5000, ChargedSeconds: 3600})
	for _, seed := range []struct {
		table string         // 原订单及冻结证据所属表
		row   map[string]any // 原身份、时刻及人工最终费用
	}{
		{"payment_order", map[string]any{"id": id, "order_no": "P-" + no, "user_id": id, "biz_type": "charge", "biz_id": id, "pay_method": "wechat", "status": "paid", "total_cents": 10000, "paid_cents": 10000, "created_month": "2026-10-01"}},
		{"charge_order", map[string]any{"id": id, "order_no": no, "user_id": id, "device_id": no, "port_no": 1, "status": "completed", "payment_status": "paid", "payment_order_id": id, "started_at": at.Add(-time.Hour), "ended_at": at, "created_month": "2026-10-01"}},
		{"charge_order_pricing", map[string]any{"charge_order_id": id, "payment_intent_id": uuid.NewString(), "user_id": id, "port_code": no, "pricing_snapshot": string(contract)}},
		{"charge_end_receipt", map[string]any{"charge_order_id": id, "stop_command_id": uuid.NewString(), "meter_json": string(meter)}},
		{"charge_manual_settlement", map[string]any{"charge_order_id": id, "request_id": uuid.NewString(), "actor_id": id, "electric_cents": 300, "service_cents": 100, "reason": "proof"}},
		{"charge_billing_job", map[string]any{"charge_order_id": id, "status": "pending"}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(err)
		}
	}
	source, err := (BillingOrders{DB: tx}).Read(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	fee, err := PriceSource(source)
	if err != nil {
		t.Fatal(err)
	}
	result := Result{Source: source, CalculationNo: CalculationNumber(no, id), ActualFee: fee}
	fault := errors.New("marketing rules unavailable")
	if err := tx.Callback().Query().Before("gorm:query").Register("test:billing-marketing", func(q *gorm.DB) {
		if q.Statement.Table == "coupon_activity_rule" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	err = (BillingOrders{DB: tx}).Apply(context.Background(), result)
	tx.Callback().Query().Remove("test:billing-marketing")
	if !errors.Is(err, fault) {
		t.Fatalf("marketing storage failure swallowed %v", err)
	}
	for _, table := range []string{"charge_fee_receipt", "charge_event_log"} {
		var n int64
		if err := tx.Table(table).Where("charge_order_id=?", id).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("partial %s %d %v", table, n, err)
		}
	}
	var n int64
	if err := tx.Table("refund_record").Where("payment_order_id=?", id).Count(&n).Error; err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if err := (BillingOrders{DB: tx}).Apply(context.Background(), result); err != nil {
		t.Fatalf("not applicable should continue %v", err)
	}
	if err := tx.Table("charge_fee_receipt").Where("charge_order_id=?", id).Count(&n).Error; err != nil || n != 1 {
		t.Fatal(n, err)
	}
}
