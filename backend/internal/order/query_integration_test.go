//go:build integration

package order_test

import (
	"chargepilot/internal/domain/charging"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/order"
	"chargepilot/internal/persist"
	"chargepilot/internal/platform/dbconn"
	"chargepilot/internal/settlement"
	"chargepilot/internal/testfixture"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// 缺失、无效费用为 nil，确认零费为非空零值，批量查询固定3次；全部造数外层回滚。
func TestOrderQueryKeepsOwnerNullFeesBatchingAndFrozenNames(t *testing.T) {
	tx := queryDB(t).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	ctx := context.Background()
	id := testfixture.NewID()
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
		Rule  charging.Rule  `json:"rule"`  /* 既有规则 */
		Offer charging.Offer `json:"offer"` /* 冻结套餐 */
	}{Rule: charging.Rule{Version: 1, Spec: charging.Spec{Scheme: &charging.Scheme{Name: "Original frozen scheme"}}}, Offer: charging.Offer{ID: 1, StationID: id, Name: "Original frozen package", Mode: "duration", PriceCents: 100, DurationMinutes: 30}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("charge_order_pricing").Create(map[string]any{"charge_order_id": id, "payment_intent_id": uuid.NewString(), "user_id": id, "port_code": tag, "pricing_snapshot": string(snapshot)}).Error; err != nil {
		t.Fatal(err)
	}
	reader := operations.Profile{Permissions: []string{"order.read"}}
	for _, permissions := range [][]string{{"order.read"}, {"order.read", "order.refund.create"}} {
		actor := operations.Profile{ID: 8996759476100805168, Permissions: permissions}
		view, err := s.Detail(ctx, actor, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(permissions) == 1 && view.RefundApplicantID != nil {
			t.Fatal("refund identity leaked without permission")
		}
		if len(permissions) == 2 && (view.RefundApplicantID == nil || *view.RefundApplicantID != "8996759476100805168") {
			t.Fatal("refund applicant decimal identity missing")
		}
	}
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

// TestOrderListQueryTimeKeepsClosedBoundsAndStartedPriority 验证生成查询时间的闭区间、未启动回退、启动优先及更新即时生效。
// 真实订单均使用私有设备编号，在外层事务回滚，不依赖开发订单内容。
func TestOrderListQueryTimeKeepsClosedBoundsAndStartedPriority(t *testing.T) {
	tx := queryDB(t).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	from, to := month.Add(time.Hour), month.Add(2*time.Hour)
	id, board := testfixture.NewID(), "QT-"+uuid.NewString()
	seeds := []struct {
		created  time.Time
		started  *time.Time
		included bool
	}{
		{created: from, included: true},
		{created: to, included: true},
		{created: from.Add(-time.Millisecond)},
		{created: to.Add(time.Millisecond)},
		{created: from.Add(-time.Hour), started: &to, included: true},
		{created: from, started: timePointer(to.Add(time.Millisecond))},
	}
	expected := map[uint64]bool{}
	for i, seed := range seeds {
		oid := id + uint64(i)
		if err := tx.Table("charge_order").Create(map[string]any{"id": oid, "order_no": fmt.Sprintf("%s-%d", board, i), "user_id": id, "device_id": board, "port_no": 1, "created_month": month, "created_at": seed.created, "started_at": seed.started}).Error; err != nil {
			t.Fatal(err)
		}
		if seed.included {
			expected[oid] = true
		}
	}
	// 不同时区的相同时间瞬间仍按UTC筛选。
	lower, upper := from.In(time.FixedZone("fixture", 8*60*60)), to.In(time.FixedZone("fixture", 8*60*60))
	q := order.OrderQuery{PageQuery: persist.PageQuery{Page: 1, PageSize: 20}, DeviceID: board, From: &lower, To: &upper}
	assertIDs := func() {
		t.Helper()
		out, err := order.ListOrders(t.Context(), tx, q)
		if err != nil || out.Total != int64(len(expected)) || len(out.Items) != len(expected) {
			t.Fatalf("查询总数=%d items=%d want=%d err=%v", out.Total, len(out.Items), len(expected), err)
		}
		seen := map[uint64]bool{}
		for _, row := range out.Items {
			if !expected[row.OrderID] || seen[row.OrderID] {
				t.Fatalf("返回意外/重复订单 %d", row.OrderID)
			}
			seen[row.OrderID] = true
		}
	}
	assertIDs()
	if err := tx.Table("charge_order").Where("id=? AND created_month=?", id, month).Update("started_at", to.Add(time.Millisecond)).Error; err != nil {
		t.Fatal(err)
	}
	delete(expected, id)
	assertIDs()
	if err := tx.Table("charge_order").Where("id=? AND created_month=?", id, month).Update("started_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	expected[id] = true
	assertIDs()
}

// timePointer 返回私有测试时间指针，用于区分未启动NULL和真实开始时刻。
func timePointer(at time.Time) *time.Time { return &at }
