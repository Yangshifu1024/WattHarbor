//go:build integration

package order_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/order"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"os"
	"testing"
	"time"
)

// queryDB 为 t 打开独立 TEST_MYSQL_URL，缺配置或失败终止；退出只关闭自身连接池。
func queryDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_URL")
	if raw == "" {
		t.Fatal("TEST_MYSQL_URL is required")
	}
	pool, err := dbconn.Open(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	db, err := dbconn.WrapGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// TestOrderQueryKeepsOwnerNullFeesBatchingAndFrozenNames 使用真实订单、结算回执与站点读取验证所属只读组合。
// 缺失／无效费用为 nil，确认零费为非空零值，批量查询固定3次；全部造数外层回滚。
func TestOrderQueryKeepsOwnerNullFeesBatchingAndFrozenNames(t *testing.T) {
	tx := queryDB(t).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	ctx := context.Background()
	id := uint64(time.Now().UnixNano())
	tag := uuid.NewString()
	month := time.Now().UTC().Format("2006-01") + "-01"
	for i := range 12 {
		status := "completed"
		if i == 11 {
			status = "paid"
		}
		if err := tx.Table("charge_order").Create(map[string]any{"id": id + uint64(i), "order_no": fmt.Sprintf("Q%s%d", tag, i), "user_id": id, "device_id": "query-proof", "port_no": 1, "status": status, "created_month": month}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Table("charge_order").Create(map[string]any{"id": id + 20, "order_no": "Q" + tag + "other", "user_id": id + 1, "device_id": "query-proof", "port_no": 1, "status": "charging", "created_month": month}).Error; err != nil {
		t.Fatal(err)
	}
	for i, raw := range []string{"{\"electric_cents\":3,\"service_cents\":4,\"total_cents\":7}", "{\"electric_cents\":0,\"service_cents\":0,\"total_cents\":0}", "\"invalid-receipt-shape\""} {
		if err := tx.Table("charge_fee_receipt").Create(map[string]any{"charge_order_id": id + uint64(i), "calculation_no": fmt.Sprintf("QB%s%d", tag, i), "result_json": raw}).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := order.QueryService{DB: tx, Fees: settlement.QueryFeeRepo{DB: tx}}
	count := 0
	const callback = "order_query_batch_count"
	if err := tx.Callback().Query().After("gorm:query").Register(callback, func(q *gorm.DB) { count++ }); err != nil {
		t.Fatal(err)
	}
	defer tx.Callback().Query().Remove(callback)
	for _, size := range []int{1, 20} {
		count = 0
		out, err := s.History(ctx, id, persist.PageQuery{Page: 1, PageSize: size})
		if err != nil || out.Total != 12 || count != 3 {
			t.Fatalf("history size=%d total=%d SQL=%d err=%v", size, out.Total, count, err)
		}
	}
	tx.Callback().Query().Remove(callback)
	out, err := s.History(ctx, id, persist.PageQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[uint64]order.UserOrderHistoryRow{}
	for _, r := range out.Items {
		byID[r.OrderID] = r
	}
	if byID[id].TotalCents == nil || *byID[id].TotalCents != 7 || byID[id+1].TotalCents == nil || *byID[id+1].TotalCents != 0 || byID[id+2].TotalCents != nil || byID[id+3].TotalCents != nil {
		t.Fatal("confirmed zero, missing or invalid fee semantics changed")
	}
	if _, err := s.Resolve(ctx, id, "Q"+tag+"other"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("owner isolation=%v", err)
	}
	ongoing, err := s.Ongoing(ctx, id)
	if err != nil || ongoing.ID != id+11 || ongoing.Status != "paid" {
		t.Fatalf("latest ongoing=%+v err=%v", ongoing, err)
	}
	if err := tx.Table("station").Create(map[string]any{"id": id, "name": "Original station", "longitude": 0, "latitude": 0, "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
	rows := []order.OrderView{{OrderID: id, StationID: &id}}
	if err := order.FillOrderStations(ctx, tx, rows); err != nil || rows[0].StationName == nil || *rows[0].StationName != "Original station" {
		t.Fatalf("owned station projection=%+v err=%v", rows, err)
	}
	snapshot, err := json.Marshal(struct {
		Rule  charging.Rule  `json:"rule"`  /* 原规则 */
		Offer charging.Offer `json:"offer"` /* 原冻结套餐 */
	}{Rule: charging.Rule{Version: 1, Spec: charging.Spec{Scheme: &charging.Scheme{Name: "Original frozen scheme"}}}, Offer: charging.Offer{ID: 1, StationID: id, Name: "Original frozen package", Mode: "duration", PriceCents: 100, DurationMinutes: 30}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("charge_order_pricing").Create(map[string]any{"charge_order_id": id, "payment_intent_id": uuid.NewString(), "user_id": id, "port_code": tag, "pricing_snapshot": string(snapshot)}).Error; err != nil {
		t.Fatal(err)
	}
	reader := operations.Profile{Permissions: []string{"order.read"}}
	detail, err := s.Detail(ctx, reader, id)
	if err != nil || detail.SelectedSchemeName == nil || *detail.SelectedSchemeName != "Original frozen scheme" || detail.SelectedPackageName == nil || *detail.SelectedPackageName != "Original frozen package" {
		t.Fatalf("frozen detail=%+v err=%v", detail, err)
	}
	if err := tx.Table("charge_event_log").Create(map[string]any{"charge_order_id": id, "event_id": tag, "event": "proof", "actor": "query", "detail": "original event", "occurred_at": time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	events, err := s.Timeline(ctx, reader, id)
	if err != nil || len(events) != 1 || events[0].EventID != tag {
		t.Fatalf("timeline=%+v err=%v", events, err)
	}
	if _, err := s.Timeline(ctx, reader, id+99); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing timeline=%v", err)
	}
	if _, err := s.Detail(ctx, operations.Profile{}, id); !errors.Is(err, operations.ErrForbidden) {
		t.Fatalf("unpermitted detail=%v", err)
	}
	fault := errors.New("order fee query failure")
	const faultCallback = "order_query_fee_fault"
	if err := tx.Callback().Query().Before("gorm:query").Register(faultCallback, func(q *gorm.DB) {
		if q.Statement.Table == "charge_fee_receipt" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.History(ctx, id, persist.PageQuery{Page: 1, PageSize: 20})
	tx.Callback().Query().Remove(faultCallback)
	if !errors.Is(err, fault) || !order.IsFeeQueryError(err) {
		t.Fatalf("fee stage=%v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Resolve(cancelled, id, fmt.Sprintf("Q%s%d", tag, 0)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled resolve=%v", err)
	}
}
