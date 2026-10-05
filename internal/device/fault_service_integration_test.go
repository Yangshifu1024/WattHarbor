//go:build integration

package device_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/persist"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// faultFixture 提供所属账号、角色和范围；提交造数只按本测试身份清理，资金表不参与。
func faultFixture(t *testing.T, db *gorm.DB) (uint64, string, operations.Profile) {
	t.Helper()
	id, tag := uint64(time.Now().UnixNano()), uuid.NewString()
	board := "FT" + tag
	t.Cleanup(func() {
		for _, item := range []struct {
			table, where string
			arg          any
		}{
			{"audit_log", "actor_id=?", id}, {"device_fault_report_event", "report_id IN (SELECT id FROM device_fault_report WHERE device_id=?)", board},
			{"device_fault_report", "device_id=?", board}, {"device_meta", "device_id=?", board}, {"admin_data_scope", "admin_user_id=?", id},
			{"admin_user", "id=?", id}, {"role_permission", "role_id=?", id}, {"role", "id=?", id},
		} {
			if err := db.Table(item.table).Where(item.where, item.arg).Delete(nil).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := db.Table(item.table).Where(item.where, item.arg).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("%s remaining=%d %v", item.table, n, err)
			}
		}
	})
	for _, seed := range []struct {
		table string
		row   map[string]any
	}{
		{"role", map[string]any{"id": id, "code": tag, "name": "fault proof", "is_builtin": false}},
		{"admin_user", map[string]any{"id": id, "username": tag, "password_hash": "fixture", "role_id": id, "status": "active", "auth_version": 1}},
		{"device_meta", map[string]any{"device_id": board, "station_id": id, "vendor_id": id, "status": "enabled", "protocol_adapter": "dc589"}},
	} {
		if err := db.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(seed.table, err)
		}
	}
	for _, code := range []string{"fault.read", "fault.dispatch", "fault.resolve"} {
		var pid uint64
		if err := db.Table("permission").Where("code=?", code).Pluck("id", &pid).Error; err != nil || pid == 0 {
			t.Fatal(code, err)
		}
		if err := db.Table("role_permission").Create(map[string]any{"role_id": id, "permission_id": pid}).Error; err != nil {
			t.Fatal(err)
		}
	}
	actor, err := (ops.AdminStore{DB: db}).Profile(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return id, board, actor
}

// TestFaultTransitionsKeepAtomicAuditAndCurrentScope 验证报告事件、工单状态和审计一起回滚，范围内外与用户可见性一致。
func TestFaultTransitionsKeepAtomicAuditAndCurrentScope(t *testing.T) {
	db := importDB(t, "TEST_MYSQL_URL")
	id, board, actor := faultFixture(t, db)
	s, ctx := device.FaultService{DB: db}, context.Background()
	in := device.FaultSubmission{DeviceID: board, FaultType: "other", PortNo: 9}
	fault := errors.New("fault event write unavailable")
	key := "fault_atomic_" + uuid.NewString()
	if err := db.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "device_fault_report_event" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Report(ctx, id, in)
	db.Callback().Create().Remove(key)
	if !errors.Is(err, fault) {
		t.Fatal(err)
	}
	var n int64
	if err := db.Table("device_fault_report").Where("device_id=?", board).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("partial report=%d %v", n, err)
	}
	out, err := s.Report(ctx, id, in)
	if err != nil {
		t.Fatal(err)
	}
	otherID, _, _ := faultFixture(t, db)
	if err := db.Table("admin_data_scope").Create(map[string]any{"admin_user_id": otherID, "scope_type": "station", "scope_id": otherID, "created_by": otherID}).Error; err != nil {
		t.Fatal(err)
	}
	var input *device.FaultInputError
	if err := s.Dispatch(ctx, ops.AuditContext{Actor: actor}, out.ID, device.FaultDispatch{AssignedTo: otherID}); !errors.As(err, &input) {
		t.Fatalf("outside assignee=%v", err)
	}
	var row struct {
		Status     string
		AssignedTo *uint64
		ImagesJSON string
		ResolvedAt *time.Time
	}
	read := func() {
		t.Helper()
		if err := db.Table("device_fault_report").Where("id=?", out.ID).Take(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	read()
	if row.AssignedTo != nil || row.ImagesJSON != "null" {
		t.Fatalf("initial=%+v", row)
	}
	if err := db.Table("device_fault_report_event").Create(map[string]any{"report_id": out.ID, "event_type": "migration_baseline", "user_visible": false, "note": "private"}).Error; err != nil {
		t.Fatal(err)
	}
	history, err := s.UserHistory(ctx, id, out.ID)
	if err != nil || len(history) != 1 {
		t.Fatalf("visible=%v %v", history, err)
	}
	if _, err := s.UserHistory(ctx, id+1, out.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal(err)
	}
	audit := ops.AuditContext{Actor: actor, RequestID: uuid.NewString()}
	if err := db.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "audit_log" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	err = s.Dispatch(ctx, audit, out.ID, device.FaultDispatch{AssignedTo: id})
	db.Callback().Create().Remove(key)
	if !errors.Is(err, fault) {
		t.Fatal(err)
	}
	read()
	if row.Status != "open" || row.AssignedTo != nil {
		t.Fatalf("partial dispatch=%+v", row)
	}
	for range 2 {
		if err := s.Dispatch(ctx, audit, out.ID, device.FaultDispatch{AssignedTo: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Resolve(ctx, audit, out.ID, device.FaultResolution{Status: "closed"}); !errors.Is(err, operations.ErrConflict) {
		t.Fatal(err)
	}
	note := "真实修复说明"
	fixed := device.FaultResolution{Status: "fixed", Note: &note}
	if err := db.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "audit_log" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	err = s.Resolve(ctx, audit, out.ID, fixed)
	db.Callback().Create().Remove(key)
	if !errors.Is(err, fault) {
		t.Fatal(err)
	}
	read()
	if row.Status != "dispatched" || row.ResolvedAt != nil {
		t.Fatalf("partial resolve=%+v", row)
	}
	if err := s.Resolve(ctx, audit, out.ID, fixed); err != nil {
		t.Fatal(err)
	}
	read()
	at := *row.ResolvedAt
	if err := s.Resolve(ctx, audit, out.ID, fixed); err != nil {
		t.Fatal(err)
	}
	read()
	if !row.ResolvedAt.Equal(at) {
		t.Fatal("replay changed resolved time")
	}
	for range 2 {
		if err := s.Resolve(ctx, audit, out.ID, device.FaultResolution{Status: "closed"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Table("audit_log").Where("actor_id=? AND target_id=?", id, fmt.Sprint(out.ID)).Count(&n).Error; err != nil || n != 3 {
		t.Fatalf("audits=%d %v", n, err)
	}
	history, err = s.UserHistory(ctx, id, out.ID)
	if err != nil || len(history) != 4 {
		t.Fatalf("visible transitions=%d %v", len(history), err)
	}
	if err := db.Table("admin_data_scope").Create(map[string]any{"admin_user_id": id, "scope_type": "station", "scope_id": id + 1, "created_by": id}).Error; err != nil {
		t.Fatal(err)
	}
	q := persist.PageQuery{Page: 1, PageSize: 20}
	page, err := s.List(ctx, actor, q)
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("outside list=%+v %v", page, err)
	}
	if _, err := s.History(ctx, actor, out.ID, q); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal(err)
	}
	if err := s.Resolve(ctx, audit, out.ID, device.FaultResolution{Status: "closed"}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal(err)
	}
	if err := db.Table("admin_data_scope").Where("admin_user_id=?", id).Update("scope_id", id).Error; err != nil {
		t.Fatal(err)
	}
	page, err = s.List(ctx, actor, q)
	if err != nil || page.Total != 1 {
		t.Fatalf("inside=%+v %v", page, err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "device_fault_report" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.UserHistory(ctx, id, out.ID)
	db.Callback().Query().Remove(key)
	if !errors.Is(err, fault) {
		t.Fatalf("storage lost=%v", err)
	}
}

// TestConcurrentFaultDispatchKeepsOneEventAndSeesRevocation 两独立连接在同一工单锁闸门下验证重放只写一次，等待者读取最新处理人停用。
func TestConcurrentFaultDispatchKeepsOneEventAndSeesRevocation(t *testing.T) {
	setup, first, second := importDB(t, "TEST_MYSQL_URL"), importDB(t, "TEST_MYSQL_URL"), importDB(t, "TEST_MYSQL_URL")
	id, board, actor := faultFixture(t, setup)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := (device.FaultService{DB: setup}).Report(ctx, id, device.FaultSubmission{DeviceID: board, FaultType: "other"})
	if err != nil {
		t.Fatal(err)
	}
	locked, trying, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var one, two, pass sync.Once
	allow := func() { pass.Do(func() { close(release) }) }
	defer allow()
	key := "fault_lock_" + uuid.NewString()
	match := func(q *gorm.DB) bool {
		_, ok := q.Statement.Clauses["FOR"]
		return ok && q.Statement.Table == "device_fault_report"
	}
	if err := first.Callback().Query().After("gorm:query").Register(key, func(q *gorm.DB) {
		if match(q) {
			one.Do(func() { close(locked); <-release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer first.Callback().Query().Remove(key)
	if err := second.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if match(q) {
			two.Do(func() { close(trying) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer second.Callback().Query().Remove(key)
	call := func(db *gorm.DB) chan error {
		ch := make(chan error, 1)
		go func() {
			ch <- (device.FaultService{DB: db}).Dispatch(ctx, ops.AuditContext{Actor: actor, RequestID: uuid.NewString()}, out.ID, device.FaultDispatch{AssignedTo: id})
		}()
		return ch
	}
	a := call(first)
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	b := call(second)
	select {
	case <-trying:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	allow()
	if err := <-a; err != nil {
		t.Fatal(err)
	}
	if err := <-b; err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := setup.Table("device_fault_report_event").Where("report_id=? AND event_type='dispatched'", out.ID).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("dispatch=%d %v", n, err)
	}
	first.Callback().Query().Remove(key)
	second.Callback().Query().Remove(key)
	// 先建立RC事务的范围读取，再停用处理人；锁后Profile不得使用旧RR快照。
	pre, proceed := make(chan struct{}), make(chan struct{})
	var gate sync.Once
	if err := second.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if match(q) {
			gate.Do(func() { close(pre); <-proceed })
		}
	}); err != nil {
		t.Fatal(err)
	}
	done := call(second)
	select {
	case <-pre:
	case <-ctx.Done():
		close(proceed)
		t.Fatal(ctx.Err())
	}
	if err := setup.Table("admin_user").Where("id=?", id).Update("status", "disabled").Error; err != nil {
		close(proceed)
		t.Fatal(err)
	}
	close(proceed)
	var input *device.FaultInputError
	if err := <-done; !errors.As(err, &input) {
		t.Fatalf("revoked assignee=%v", err)
	}
}
