//go:build integration

package device_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestConcurrentDeviceMetadataKeepsOneVersion 用两条独立连接和 FROM 行锁闸门核对相同旧表单只能成功一次。
// 真库提交只清理自身设备与请求号并核对零残留；身份权限为 fixture，版本及审计使用真实 SQL。
func TestConcurrentDeviceMetadataKeepsOneVersion(t *testing.T) {
	setup, first, second := importDB(t, "TEST_MYSQL_URL"), importDB(t, "TEST_MYSQL_URL"), importDB(t, "TEST_MYSQL_URL")
	id, board := testfixture.NewID(), "ME"+uuid.NewString()
	requests := []string{uuid.NewString(), uuid.NewString()}
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	defer func() {
		for _, q := range []struct {
			table string /* 本测试提交的表 */
			where string /* 原身份条件 */
			arg   any    /* 本测试身份 */
		}{
			{"audit_log", "request_id IN ?", requests}, {"device_meta", "device_id=?", board},
		} {
			var cleanupErr error
			if q.table == "audit_log" {
				cleanupErr = cleanupDeviceAudits(context.Background(), setup, requests)
			} else {
				cleanupErr = setup.Table(q.table).Where(q.where, q.arg).Delete(nil).Error
			}
			if err := cleanupErr; err != nil {
				t.Error(err)
			}
			var n int64
			if err := setup.Table(q.table).Where(q.where, q.arg).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("%s remaining=%d %v", q.table, n, err)
			}
		}
	}()
	if err := setup.Table("device_meta").Create(map[string]any{"device_id": board, "status": "enabled", "updated_at": at, "protocol_adapter": "dc589"}).Error; err != nil {
		t.Fatal(err)
	}
	locked, trying, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once, attempt, pass sync.Once
	allow := func() { pass.Do(func() { close(release) }) }
	defer allow()
	key := "metadata_lock_" + uuid.NewString()
	match := func(q *gorm.DB) bool {
		_, lock := q.Statement.Clauses["FOR"]
		return lock && q.Statement.TableExpr != nil && q.Statement.TableExpr.SQL == "device_meta AS d"
	}
	if err := first.Callback().Query().After("gorm:query").Register(key, func(q *gorm.DB) {
		if match(q) {
			once.Do(func() { close(locked); <-release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer first.Callback().Query().Remove(key)
	if err := second.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if match(q) {
			attempt.Do(func() { close(trying) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer second.Callback().Query().Remove(key)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// answer 传递资料更新用例返回的设备行与错误，区分首次更新和旧版本冲突；最终资料及审计通过真实表确认。
	type answer struct {
		row device.Device /* 更新结果 */
		err error         /* 原事务错误 */
	}
	call := func(db *gorm.DB, request, model string) chan answer {
		ch := make(chan answer, 1)
		go func() {
			modelJSON, _ := json.Marshal(model)
			versionJSON, _ := json.Marshal(at.Format(time.RFC3339Nano))
			row, err := (device.ManagementService{DB: db}).Update(ctx, ops.AuditContext{Actor: operations.Profile{ID: id, Permissions: []string{"device.operate"}}, RequestID: request}, operations.DataScope{Unrestricted: true}, board, device.DeviceInput{Model: modelJSON, SerialNo: json.RawMessage("null"), WarrantyUntil: json.RawMessage("null"), Tags: json.RawMessage("[]"), ExpectedUpdatedAt: versionJSON})
			ch <- answer{row, err}
		}()
		return ch
	}
	a := call(first, requests[0], "首次资料")
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal("first metadata lock not reached")
	}
	b := call(second, requests[1], "旧表单覆盖")
	select {
	case <-trying:
	case <-ctx.Done():
		t.Fatal("second metadata lock not attempted")
	}
	allow()
	one, two := <-a, <-b
	if one.err != nil || !errors.Is(two.err, operations.ErrConflict) || one.row.Model == nil || *one.row.Model != "首次资料" {
		t.Fatalf("first=%+v second=%+v", one, two)
	}
	var model string
	if err := setup.Table("device_meta").Where("device_id=?", board).Pluck("model", &model).Error; err != nil || model != "首次资料" {
		t.Fatalf("model=%s %v", model, err)
	}
	var audits int64
	if err := setup.Table("audit_log").Where("request_id IN ?", requests).Count(&audits).Error; err != nil || audits != 1 {
		t.Fatalf("audits=%d %v", audits, err)
	}
}
