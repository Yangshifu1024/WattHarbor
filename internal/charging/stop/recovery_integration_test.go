//go:build integration

package stop

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	rules "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// stopTestDB 打开已迁移开发库；t 注册关闭，缺少配置或连接失败直接失败。
func stopTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw := os.Getenv("TEST_MYSQL_URL")
	if raw == "" {
		t.Fatal("TEST_MYSQL_URL 未配置，真库验收不能跳过")
	}
	db, err := dbconn.Open(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}

// TestConcurrentCutoffKeepsFirstSnapshot 用两条独立连接验证唯一键首写及当前读。
// 第一条事务先写入且持锁，第二条在首次提交后只能复用原时间、原因和费用。
func TestConcurrentCutoffKeepsFirstSnapshot(t *testing.T) {
	firstDB, secondDB := stopTestDB(t), stopTestDB(t)
	id := uint64(time.Now().UnixNano())
	t.Cleanup(func() {
		if err := firstDB.Table("charge_billing_cutoff").Where("charge_order_id=?", id).Delete(nil).Error; err != nil {
			t.Error(err)
		}
		var count int64
		if err := firstDB.Table("charge_billing_cutoff").Where("charge_order_id=?", id).Count(&count).Error; err != nil || count != 0 {
			t.Errorf("cleanup count=%d err=%v", count, err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := firstDB.WithContext(ctx).Begin()
	defer first.Rollback()
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := FreezeBillingCutoff(first, id, map[string]any{"cutoff_at": at, "reason": "budget_exhausted", "electric_cents": 70, "service_cents": 30}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	done := make(chan error, 1)
	const gate = "stop-test:before-cutoff-insert"
	if err := secondDB.Callback().Create().Before("gorm:create").Register(gate, func(tx *gorm.DB) {
		if tx.Statement.Table == "charge_billing_cutoff" {
			close(entered)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer secondDB.Callback().Create().Remove(gate)
	go func() {
		done <- secondDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := FreezeBillingCutoff(tx, id, map[string]any{"cutoff_at": at.Add(time.Hour), "reason": "automatic_stop", "electric_cents": 999, "service_cents": 999}); err != nil {
				return err
			}
			var stored struct {
				CutoffAt                    time.Time
				Reason                      string
				ElectricCents, ServiceCents int64
			}
			if err := tx.Table("charge_billing_cutoff").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", id).Take(&stored).Error; err != nil {
				return err
			}
			if !stored.CutoffAt.Equal(at) || stored.Reason != "budget_exhausted" || stored.ElectricCents != 70 || stored.ServiceCents != 30 {
				return errors.New("first cutoff snapshot overwritten")
			}
			return nil
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := first.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// seedStopOrder 在外层 tx 内创建隔离订单；id、no 是唯一身份，withCard 决定是否创建卡会话。
// t 在任一建行失败时终止；调用方回滚全部数据，不自动建表或清库。
func seedStopOrder(t *testing.T, tx *gorm.DB, id uint64, no string, withCard bool) {
	t.Helper()
	started := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Millisecond)
	month := started.Format("2006-01") + "-01"
	offer := rules.Offer{ID: 1, StationID: 1, Name: "恢复测试", Mode: "duration", PriceCents: 100, DurationMinutes: 1}
	snapshot, err := json.Marshal(struct {
		Rule  rules.Rule  `json:"rule"`
		Offer rules.Offer `json:"offer"`
	}{Offer: offer})
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct {
		table string
		row   map[string]any
	}{
		{"charge_order", map[string]any{"id": id, "order_no": no, "user_id": id, "device_id": no, "port_no": 1, "status": "charging", "payment_status": "paid", "started_at": started, "created_month": month}},
		{"charge_order_pricing", map[string]any{"charge_order_id": id, "payment_intent_id": rand.Text(), "user_id": id, "port_code": no, "pricing_snapshot": string(snapshot)}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if withCard {
		if err := tx.Table("card_charge").Create(map[string]any{"charge_order_id": id, "card_no": no, "card_id": id, "port_code": no, "wallet_after_cents": 0, "package_json": "{}", "paid_cents": 100, "purchased_minutes": 1, "max_minutes": 180}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// TestStopRecoversCommittedCutoffWithoutEvidence 以真库证明冻结后受理未知能恢复且不阻挡后单。
// 网络用 stub 故障注入，数据库冻结与重读使用真实 MySQL；全部数据外层回滚。
func TestStopRecoversCommittedCutoffWithoutEvidence(t *testing.T) {
	for _, withCard := range []bool{false, true} {
		t.Run(map[bool]string{false: "duration", true: "card"}[withCard], func(t *testing.T) {
			tx := stopTestDB(t).Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			id := uint64(time.Now().UnixNano())
			no := "C-STOP-" + rand.Text()
			seedStopOrder(t, tx, id, no, withCard)
			seedStopOrder(t, tx, id+1, no+"-2", withCard)
			fail := true
			calls := map[string]int{}
			scoped := tx.Scopes(func(q *gorm.DB) *gorm.DB {
				if q.Statement.TableExpr != nil && q.Statement.TableExpr.SQL == "charge_order c" {
					return q.Where("c.id IN ?", []uint64{id, id + 1})
				}
				return q
			})
			stopper := AutoStopper{DB: scoped, Gateway: fakeGateway{
				evidence: func(q []gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error) {
					if !fail {
						t.Fatal("frozen decisions requested new evidence")
					}
					pages := make([]gatewaycontract.EvidencePage, len(q))
					for i, r := range q {
						pages[i] = gatewaycontract.EvidencePage{DeviceID: r.DeviceID, PortNo: r.PortNo, Complete: true}
					}
					return pages, nil
				},
				stop: func(r gatewaycontract.StopRequest) (gatewaycontract.StopAcceptance, error) {
					calls[r.OrderNo]++
					if fail && r.OrderNo == no {
						return gatewaycontract.StopAcceptance{}, context.DeadlineExceeded
					}
					return gatewaycontract.StopAcceptance{Accepted: true}, nil
				},
			}}
			if n, err := stopper.Run(context.Background()); n != 1 || !errors.Is(err, context.DeadlineExceeded) || calls[no+"-2"] != 1 {
				t.Fatalf("first n=%d err=%v calls=%v", n, err, calls)
			}
			before, err := FindBillingCutoff(context.Background(), tx, id)
			if err != nil || before.CutoffAt == nil {
				t.Fatalf("cutoff=%+v err=%v", before, err)
			}
			// 清除快照，模拟恢复期间取证或计价依据不可用。截止记录已足以继续原决定。
			if err := tx.Table("charge_order_pricing").Where("charge_order_id IN ?", []uint64{id, id + 1}).Delete(nil).Error; err != nil {
				t.Fatal(err)
			}
			fail = false
			if n, err := stopper.Run(context.Background()); n != 2 || err != nil {
				t.Fatalf("resume n=%d err=%v", n, err)
			}
			after, err := FindBillingCutoff(context.Background(), tx, id)
			if err != nil || *after.CutoffAt != *before.CutoffAt || after.Reason != before.Reason {
				t.Fatalf("changed cutoff: before=%+v after=%+v err=%v", before, after, err)
			}
			if err := tx.Table("charge_order").Where("id IN ?", []uint64{id, id + 1}).Update("status", "completed").Error; err != nil {
				t.Fatal(err)
			}
			if n, err := stopper.Run(context.Background()); n != 0 || err != nil || calls[no] != 2 {
				t.Fatalf("ended n=%d err=%v calls=%v", n, err, calls)
			}
		})
	}
}
