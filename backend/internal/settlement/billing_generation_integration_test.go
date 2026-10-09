//go:build integration

package settlement

import (
	charging "chargepilot/internal/domain/charging"
	"chargepilot/internal/testfixture"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"testing"
	"time"
)

// pausedBillingRead 固定 Due 的旧代际，在真实来源读取完成后暂停；放行后仍返回旧快照。
// 内嵌仓储的 Defer 和 Apply 保持真实 SQL，避免用 mock 替代代际竞争。
type pausedBillingRead struct {
	BillingOrders               // 独立连接执行旧任务
	task          BillingTask   // 本轮已读取代际
	read, release chan struct{} // 确定性来源读取与放行闸门
}

// Due 只返回已经捕获的任务与代际，不扫描其他开发订单。
func (p pausedBillingRead) Due(context.Context) ([]BillingTask, error) {
	return []BillingTask{p.task}, nil
}

// Read 读取真实来源后通知主控，等待放行或取消；放行时保留旧来源及错误。
func (p pausedBillingRead) Read(ctx context.Context, id uint64) (Source, error) {
	source, err := p.BillingOrders.Read(ctx, id)
	close(p.read)
	select {
	case <-p.release:
		return source, err
	case <-ctx.Done():
		return Source{}, ctx.Err()
	}
}

// TestOldBillingExecutionCannotOverwriteManualRequeue 覆盖两条连接的旧读取→转人工→人工重排→旧执行恢复。
// 分别经过真实人工金额和双人计量审核，之后 Calculate/Apply 重放仅有一份计价、入账回执和余款退款。
func TestOldBillingExecutionCannotOverwriteManualRequeue(t *testing.T) {
	for _, mode := range []string{"amount", "meter"} {
		t.Run(mode, func(t *testing.T) {
			a, closeA := testConn(t)
			t.Cleanup(closeA)
			b, closeB := testConn(t)
			t.Cleanup(closeB)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			id := testfixture.NewID()
			no := "C-CAS-" + uuid.NewString()
			month := time.Now().UTC().Format("2006-01") + "-01"
			end := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
			start := end.Add(-time.Hour)
			scheme := charging.Scheme{Name: "CAS 计量", Amount: &charging.AmountMode{Algorithm: charging.ModeServerMaxPower, Periods: []charging.Period{{EndMinute: 1440, Tiers: []charging.Tier{{MaxWatts: 200, ElectricCents: 80, ServiceCents: 40}}}}}, Packages: []charging.Package{{ID: 1, Name: "金额套餐", Mode: "amount", PriceCents: 1000}}}.Normalized()
			if mode == "amount" {
				scheme = charging.Scheme{}
			}
			rule := charging.Rule{ID: 1, StationID: 1, Version: 1, Spec: charging.Spec{Scheme: &scheme}}
			if mode == "meter" {
				rule.Spec = scheme.SpecFor(charging.Package{Mode: "amount"})
				rule.Spec.Scheme = &scheme
			}
			offer := charging.Offer{ID: 101, PackageID: 1, StationID: 1, Name: "金额套餐", Mode: "amount", PriceCents: 1000, MaxMinutes: 600}
			contract, _ := json.Marshal(map[string]any{"rule": rule, "offer": offer})
			meter, _ := json.Marshal(charging.EndMeter{EndedAt: end, ChargedWh: 180, ChargedSeconds: 3600})
			t.Cleanup(func() {
				var eventIDs []string
				if err := a.Table("charge_event_log").Where("charge_order_id=?", id).Pluck("event_id", &eventIDs).Error; err != nil {
					t.Error(err)
				}
				// 退款 Outbox 没有对应订单事件日志，必须按计算号派生其独立身份清理。
				eventIDs = append(eventIDs, uuid.NewSHA1(uuid.NameSpaceURL, []byte("billing-refund:"+CalculationNumber(no, id))).String())
				if err := testfixture.DeleteOutbox(a, "event_id IN ?", eventIDs); err != nil {
					t.Error(err)
				}
				var remainingOutbox int64
				if err := a.Table("event_outbox").Where("event_id IN ?", eventIDs).Count(&remainingOutbox).Error; err != nil || remainingOutbox != 0 {
					t.Errorf("outbox residue=%d err=%v", remainingOutbox, err)
				}
				for _, table := range []string{"charge_event_log", "charge_fee_receipt", "charge_meter_review", "charge_manual_settlement", "manual_fee_review", "fee_delivery", "fee_receipt", "fee_calculation", "charge_billing_job", "charge_end_receipt", "charge_order_pricing"} {
					if err := a.Table(table).Where("charge_order_id=?", id).Delete(nil).Error; err != nil {
						t.Error(err)
					}
					var n int64
					if err := a.Table(table).Where("charge_order_id=?", id).Count(&n).Error; err != nil || n != 0 {
						t.Errorf("%s residue=%d err=%v", table, n, err)
					}
				}
				for _, q := range []struct {
					table, where string
					args         []any
				}{{"refund_record", "payment_order_id=?", []any{id}}, {"payment_order", "id=? AND created_month=?", []any{id, month}}, {"charge_order", "id=? AND created_month=?", []any{id, month}}} {
					if err := a.Table(q.table).Where(q.where, q.args...).Delete(nil).Error; err != nil {
						t.Error(err)
					}
					var n int64
					if err := a.Table(q.table).Where(q.where, q.args...).Count(&n).Error; err != nil || n != 0 {
						t.Errorf("%s residue=%d err=%v", q.table, n, err)
					}
				}
			})
			for _, seed := range []struct {
				table string
				row   map[string]any
			}{
				{"payment_order", map[string]any{"id": id, "order_no": "P-" + no, "user_id": id, "biz_type": "charge", "biz_id": id, "pay_method": "wechat", "status": "paid", "total_cents": 1000, "paid_cents": 1000, "created_month": month}},
				{"charge_order", map[string]any{"id": id, "order_no": no, "user_id": id, "device_id": no, "port_no": 1, "status": "completed", "payment_status": "paid", "payment_order_id": id, "started_at": start, "ended_at": end, "created_month": month}},
				{"charge_order_pricing", map[string]any{"charge_order_id": id, "payment_intent_id": uuid.NewString(), "user_id": id, "port_code": no, "pricing_snapshot": string(contract)}},
				{"charge_end_receipt", map[string]any{"charge_order_id": id, "stop_command_id": uuid.NewString(), "meter_json": string(meter)}},
				{"charge_billing_job", map[string]any{"charge_order_id": id, "status": "pending", "generation": 0}},
			} {
				if err := a.Table(seed.table).Create(seed.row).Error; err != nil {
					t.Fatal(err)
				}
			}
			scope := func(db *gorm.DB) *gorm.DB {
				return db.Scopes(func(q *gorm.DB) *gorm.DB {
					if q.Statement.Table == "charge_billing_job" || q.Statement.Table == "fee_delivery" {
						return q.Where("charge_order_id=?", id)
					}
					return q
				})
			}
			orders := BillingOrders{DB: a}
			old := pausedBillingRead{BillingOrders: BillingOrders{DB: b}, task: BillingTask{ChargeOrderID: id}, read: make(chan struct{}), release: make(chan struct{})}
			done := make(chan error, 1)
			go func() {
				n, err := (BillingService{Calculator: FeeCalculator{DB: scope(b)}, Orders: old}).Run(ctx)
				if n != 0 {
					err = errors.Join(err, errors.New("old execution delivered a fee"))
				}
				done <- err
			}()
			select {
			case <-old.read:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			source, err := orders.Read(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := PriceSource(source); err == nil {
				t.Fatal("fixture must require review")
			}
			if err := orders.Defer(ctx, old.task, "manual_review", "fixture proof"); err != nil {
				t.Fatal(err)
			}
			if mode == "amount" {
				if err := orders.ResolveAmount(ctx, ManualSettlement{ChargeOrderID: id, RequestID: uuid.NewString(), ActorID: 1, ElectricCents: 300, ServiceCents: 100, Reason: "fixture proof"}); err != nil {
					t.Fatal(err)
				}
			} else {
				review, err := orders.ProposeMeter(ctx, id, 1, uuid.NewString(), "fixture proof", []charging.MeterSegment{{StartedAt: start, EndedAt: end, EnergyWh: 180, PeakW: 180}})
				if err != nil {
					t.Fatal(err)
				}
				if err := orders.ReviewMeter(ctx, id, review.ID, 2, true, ""); err != nil {
					t.Fatal(err)
				}
			}
			close(old.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			var job struct {
				Status     string
				Generation uint64
				Attempts   int
			}
			if err := a.Table("charge_billing_job").Where("charge_order_id=?", id).Take(&job).Error; err != nil || job.Status != "pending" || job.Generation != 2 || job.Attempts != 1 {
				t.Fatalf("human requeue overwritten=%+v err=%v", job, err)
			}
			if err := orders.Defer(ctx, old.task, "pending", "old fallback"); !errors.Is(err, ErrStaleBillingTask) {
				t.Fatalf("stale fallback=%v", err)
			}
			current, err := orders.Read(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			calc := FeeCalculator{DB: a}
			result, err := calc.Calculate(ctx, current)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := orders.Apply(ctx, result); err != nil {
					t.Fatal(err)
				}
				if err := calc.MarkDelivered(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			want := int64(400)
			if mode == "meter" {
				want = 120
			}
			if result.TotalCents != want {
				t.Fatalf("fee=%d want=%d", result.TotalCents, want)
			}
			for _, table := range []string{"fee_calculation", "fee_receipt", "charge_fee_receipt"} {
				var n int64
				if err := a.Table(table).Where("charge_order_id=?", id).Count(&n).Error; err != nil || n != 1 {
					t.Fatalf("%s count=%d err=%v", table, n, err)
				}
			}
			var refunds struct {
				N     int64
				Cents int64
			}
			if err := a.Table("refund_record").Select("COUNT(*) AS n,COALESCE(SUM(refund_cents),0) AS cents").Where("payment_order_id=?", id).Scan(&refunds).Error; err != nil || refunds.N != 1 || refunds.Cents != 1000-want {
				t.Fatalf("refunds=%+v err=%v", refunds, err)
			}
		})
	}
}
