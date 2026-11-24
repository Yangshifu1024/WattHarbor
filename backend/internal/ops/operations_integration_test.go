//go:build integration

package ops

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"strings"
	"testing"
	"time"
	"wattharbor/internal/persist"
)

// TestOperationsAnnouncementAuditAndBeijingDashboard 用真库验证公告、审计和北京时间指标。
// 仅给自身角色赋权，来源查询限制本测试数据，全部造数外层回滚。
func TestOperationsAnnouncementAuditAndBeijingDashboard(t *testing.T) {
	db, _, _, audit, _ := managementFixture(t)
	ctx := context.Background()
	for _, code := range []string{"announcement.create", "announcement.read", "audit.read", "dashboard.read"} {
		if err := db.Exec("INSERT INTO role_permission(role_id,permission_id) SELECT ?,id FROM permission WHERE code=?", audit.Actor.RoleID, code).Error; err != nil {
			t.Fatal(err)
		}
	}
	actor, err := (AccountRepo{DB: db}).Profile(ctx, audit.Actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	audit.Actor = actor
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := audit.Actor.ID + 20
	board := fmt.Sprint("dashboard-", id)
	now := time.Now().UTC()
	local := now.In(dashboardBeijing)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, dashboardBeijing)
	start, end := today.UTC(), today.AddDate(0, 0, 1).UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	seeds := []struct {
		// table 指定测试种子表，与 row 的列和值配对。
		table string
		// row 指定本测试记录的列和值。
		row map[string]any
	}{
		{"user", map[string]any{"id": id, "openid": board, "created_at": start}},
		{"user", map[string]any{"id": id + 1, "openid": board + "deleted", "created_at": start, "deleted_at": start}},
		{"station", map[string]any{"id": id, "name": board, "status": "active", "longitude": 0, "latitude": 0}},
		{"device_meta", map[string]any{"device_id": board, "station_id": id, "vendor_id": id, "protocol_adapter": "dc589", "status": "enabled"}},
		{"alert_event", map[string]any{"id": id, "device_id": board, "metric": "proof", "created_month": month, "status": "active"}},
		{"alert_event", map[string]any{"id": id + 1, "device_id": board, "metric": "proof-resolved", "created_month": month, "status": "auto_resolved"}},
	}
	for _, seed := range seeds {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(seed.table, err)
		}
	}
	orders := []struct {
		// created、started、ended 分别为创建、开始、结束时间；零结束时间表示尚未结束。
		created, started, ended time.Time
		// status 该测试充电订单状态，与时间范围共同决定统计归属。
		status string
		// fee 是订单总费用，单位分；nil 表示 SQL NULL，用于核对汇总空值处理。
		fee any
	}{
		{start.Add(-time.Millisecond), start.Add(-time.Millisecond), start.Add(-time.Millisecond), "completed", int64(120)},
		{start, start, start, "completed", int64(0)},
		{end.Add(-time.Millisecond), start, end.Add(-time.Millisecond), "completed", nil},
		{start, start, time.Time{}, "charging", nil},
		{end, end, end, "completed", int64(500)},
	}
	for i, row := range orders {
		seed := map[string]any{"id": id + uint64(i), "order_no": board + strconv.Itoa(i), "user_id": id, "device_id": board, "port_no": i + 1, "status": row.status, "created_at": row.created, "started_at": row.started, "total_cents": row.fee, "created_month": month}
		if !row.ended.IsZero() {
			seed["ended_at"] = row.ended
		}
		if err := tx.Table("charge_order").Create(seed).Error; err != nil {
			t.Fatal(err)
		}
	}
	key := "dashboard-own-" + board
	if err := db.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		var expr clause.Expression
		switch q.Statement.Table {
		case "charge_order":
			expr = clause.Eq{Column: "user_id", Value: id}
		case "user":
			expr = clause.IN{Column: "id", Values: []any{id, id + 1}}
		case "station":
			expr = clause.Eq{Column: "id", Value: id}
		case "device_meta", "alert_event":
			expr = clause.Eq{Column: "device_id", Value: board}
		}
		if expr != nil {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{expr}})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove(key)
	m, err := (DashboardQueryService{DB: tx}).Read(ctx, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	if m.ChargingOrders != 1 || m.TodayOrders != 3 || m.TotalUsers != 1 || m.NewUsers != 1 || m.TodayChargingUsers != 1 || m.TodayOrderUsers != 1 || m.StationCount != 1 || m.DeviceCount != 1 || m.ActiveAlerts != 1 || m.TodayCompletedOrders != 1 || m.TodaySettledCents != 0 || len(m.DailyTrend) != 7 || m.DailyTrend[5].SettledCents != 120 || m.DailyTrend[6].CompletedOrders != 1 || !m.UpdatedAt.Equal(now) {
		t.Fatalf("metrics %+v", m)
	}
	for i, row := range m.DailyTrend {
		expected := today.AddDate(0, 0, i-6).Format("2006-01-02")
		if row.Day != expected {
			t.Fatalf("day[%d]=%s want=%s", i, row.Day, expected)
		}
	}
	service := AnnouncementService{DB: tx}
	in := AnnouncementInput{Title: "原公告", Content: "中文正文", Scope: "station", TargetIDs: []string{strconv.FormatUint(id, 10), "city-original"}, StartAt: now.Add(-time.Hour)}
	announcement, err := service.Create(ctx, audit, in)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := service.List(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.ID == announcement {
			found = true
			if row.EndAt != nil || row.Status != "published" || !strings.Contains(string(row.TargetIDs), strconv.FormatUint(id, 10)) {
				t.Fatalf("announcement %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("announcement absent")
	}
	in.Scope = "global"
	in.TargetIDs = nil
	global, err := service.Create(ctx, audit, in)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = service.List(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == global && string(row.TargetIDs) != "null" {
			t.Fatalf("original null targets=%s", row.TargetIDs)
		}
	}
	audits, err := (AuditQueryService{DB: tx}).List(ctx, actor, AuditQuery{Module: "announcement", Actor: actor.Username}, persist.PageQuery{Page: 1, PageSize: 100})
	if err != nil || audits.Total != 2 {
		t.Fatalf("audits=%+v %v", audits, err)
	}
	for _, row := range audits.Items {
		if row.BeforeJSON == nil || *row.BeforeJSON != "null" || row.AfterJSON == nil || !strings.Contains(*row.AfterJSON, `"status": "published"`) {
			t.Fatalf("audit snapshots %+v", row)
		}
	}
	failure := errors.New("operations audit fixture")
	auditKey := "operations-audit-" + board
	if err := db.Callback().Create().Before("gorm:create").Register(auditKey, func(q *gorm.DB) {
		if q.Statement.Table == "audit_log" {
			q.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	in.Title = "rollback-" + board
	_, err = service.Create(ctx, audit, in)
	db.Callback().Create().Remove(auditKey)
	if !errors.Is(err, failure) {
		t.Fatalf("audit accepted %v", err)
	}
	var count int64
	if err := tx.Table("announcement").Where("title=?", in.Title).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("partial announcement %d %v", count, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (DashboardQueryService{DB: tx}).Read(cancelled, actor, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
	// 与本请求同连接撤销真实权限，旧档案也不得继续发布。
	if err := tx.Exec("DELETE rp FROM role_permission rp JOIN permission p ON p.id=rp.permission_id WHERE rp.role_id=? AND p.code='announcement.create'", actor.RoleID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, audit, in); !errors.Is(err, ErrPermission) {
		t.Fatalf("revoked publication %v", err)
	}
}
