//go:build integration

package device_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/device"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/pricing"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// concurrentImportGateway 只确认固定厂商及开通次数；并发测试证明中央锁和持久凭据，不代表设备开通。
type concurrentImportGateway struct {
	vendor uint64       // 测试唯一厂商身份
	calls  atomic.Int32 // 实际执行开通的调用数量
}

// TestOverlappingImportListsUseOneLockOrder 以反向的两份原清单并发恢复相同已登记设备，核对中央元数据行锁顺序。
// 真库持有第一台行锁后才放行另一事务；网关为 fixture，原请求数组不改写，已提交造数仅按本测试身份清理。
func TestOverlappingImportListsUseOneLockOrder(t *testing.T) {
	setup, first, second := importDB(t, "TEST_MYSQL_URL"), importDB(t, "TEST_MYSQL_URL"), importDB(t, "TEST_MYSQL_URL")
	id := uint64(time.Now().UnixNano())
	boards := []string{"AO" + strings.ReplaceAll(uuid.NewString(), "-", "")[:26], "ZO" + strings.ReplaceAll(uuid.NewString(), "-", "")[:26]}
	requests := []string{uuid.NewString(), uuid.NewString()}
	defer func() {
		for _, q := range []struct {
			table string /* 原提交所属表 */
			where string /* 本测试筛选条件 */
			arg   any    /* 唯一测试身份 */
		}{
			{"audit_log", "request_id IN ?", requests}, {"device_import", "import_id IN ?", requests}, {"device_import_identity", "device_id IN ?", boards}, {"device_meta", "device_id IN ?", boards}, {"station", "id=?", id},
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
	if err := setup.Table("station").Create(map[string]any{"id": id, "name": boards[0], "longitude": 0, "latitude": 0, "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, board := range boards {
		if err := setup.Table("device_meta").Create(map[string]any{"device_id": board, "station_id": id, "vendor_id": id, "model": "保留原资料", "status": "enabled", "protocol_adapter": "dc589"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	locked, trying, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once, attempt, pass sync.Once
	allow := func() { pass.Do(func() { close(release) }) }
	defer allow()
	key := "overlap_import_" + uuid.NewString()
	lockBoard := func(q *gorm.DB) string {
		if q.Statement.Table != "device_meta" {
			return ""
		}
		if _, ok := q.Statement.Clauses["FOR"]; !ok {
			return ""
		}
		where, ok := q.Statement.Clauses["WHERE"].Expression.(clause.Where)
		if !ok {
			return ""
		}
		for _, part := range where.Exprs {
			if expr, ok := part.(clause.Expr); ok {
				for _, arg := range expr.Vars {
					if board, ok := arg.(string); ok && (board == boards[0] || board == boards[1]) {
						return board
					}
				}
			}
		}
		return ""
	}
	if err := first.Callback().Query().After("gorm:query").Register(key, func(q *gorm.DB) {
		if lockBoard(q) == boards[0] {
			once.Do(func() { close(locked); <-release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer first.Callback().Query().Remove(key)
	if err := second.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if lockBoard(q) == boards[0] {
			attempt.Do(func() { close(trying) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer second.Callback().Query().Remove(key)
	// 若回退为请求数组顺序，第二事务先拿到后一台锁才通知，保证变异能复现真正的反向等待。
	if err := second.Callback().Query().After("gorm:query").Register(key+"_reverse", func(q *gorm.DB) {
		if lockBoard(q) == boards[1] {
			attempt.Do(func() { close(trying) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer second.Callback().Query().Remove(key + "_reverse")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gateway := &concurrentImportGateway{vendor: id}
	scope := operations.DataScope{Unrestricted: true}
	type answer struct {
		out device.ImportJob /* 原任务状态 */
		err error            /* 执行错误 */
	}
	call := func(db *gorm.DB, request string, ordered []string) chan answer {
		ch := make(chan answer, 1)
		go func() {
			in := device.ImportInput{ImportID: request}
			for _, board := range ordered {
				in.Devices = append(in.Devices, device.ImportDevice{DeviceID: board, VendorID: id, StationID: id, PortCount: 2})
			}
			out, err := (device.ImportService{DB: db, Gateway: gateway, Compatibility: pricing.Repo{DB: db}, LoadScope: func(context.Context, operations.Profile) (operations.DataScope, error) { return scope, nil }}).Create(ctx, ops.AuditContext{Actor: operations.Profile{ID: id, Permissions: []string{"device.import"}}, RequestID: request}, scope, in)
			ch <- answer{out, err}
		}()
		return ch
	}
	a := call(first, requests[0], boards)
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal("first overlap lock not reached")
	}
	b := call(second, requests[1], []string{boards[1], boards[0]})
	select {
	case <-trying:
	case <-ctx.Done():
		t.Fatal("second overlap lock not attempted")
	}
	allow()
	one, two := <-a, <-b
	if one.err != nil || two.err != nil || one.out.Status != "completed" || two.out.Status != "completed" || gateway.calls.Load() != 2 {
		t.Fatalf("overlap first=%s/%s err=%v second=%s/%s err=%v calls=%d", one.out.ImportID, one.out.Status, one.err, two.out.ImportID, two.out.Status, two.err, gateway.calls.Load())
	}
	var changed int64
	if err := setup.Table("device_meta").Where("device_id IN ? AND model<>'保留原资料'", boards).Count(&changed).Error; err != nil || changed != 0 {
		t.Fatalf("existing metadata overwritten=%d %v", changed, err)
	}
	for i, request := range requests {
		var raw string
		if err := setup.Table("device_import").Where("import_id=?", request).Pluck("request_json", &raw).Error; err != nil {
			t.Fatal(err)
		}
		var frozen []device.ImportDevice
		if err := json.Unmarshal([]byte(raw), &frozen); err != nil || len(frozen) != 2 || frozen[0].DeviceID != boards[i] {
			t.Fatalf("original input order changed=%s %v", raw, err)
		}
	}
}

// Vendor 在 ctx 下返回 id 的固定公开厂商，requestID 仅为追踪；取消或身份不符返回错误，无写入。
func (g *concurrentImportGateway) Vendor(ctx context.Context, id uint64, requestID string) (contract.VendorReply, error) {
	if err := ctx.Err(); err != nil {
		return contract.VendorReply{}, err
	}
	if id != g.vendor {
		return contract.VendorReply{}, errors.New("unknown fixture vendor")
	}
	return contract.VendorReply{ID: id, Status: "enabled", Protocol: "tcp", AdapterClass: "dc589"}, nil
}

// Provision 在 ctx 下确认原 devices，并增加次数；取消返回原错误，不访问网关数据库。
func (g *concurrentImportGateway) Provision(ctx context.Context, devices []contract.ProvisionDevice) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.calls.Add(1)
	return nil
}

// TestConcurrentFirstImportClaimsOriginalIdentity 用两条独立连接同时进入首次登记 SQL，核对唯一键等待及原请求重放。
// 首次插入持锁时第二连接已尝试原 SQL；不使用延时。中央真实提交，网关为明确 fixture，造数清理为零。
func TestConcurrentFirstImportClaimsOriginalIdentity(t *testing.T) {
	setup, first, second := importDB(t, "TEST_MYSQL_URL"), importDB(t, "TEST_MYSQL_URL"), importDB(t, "TEST_MYSQL_URL")
	id, board, request := uint64(time.Now().UnixNano()), "IC"+strings.ReplaceAll(uuid.NewString(), "-", "")[:26], uuid.NewString()
	defer func() {
		for _, q := range []struct {
			table string /* 原提交所属表 */
			where string /* 本测试身份筛选 */
			arg   any    /* 本测试唯一身份 */
		}{
			{"audit_log", "request_id=?", request}, {"device_import", "import_id=?", request}, {"device_import_identity", "device_id=?", board}, {"device_meta", "device_id=?", board}, {"station", "id=?", id},
		} {
			var cleanupErr error
			if q.table == "audit_log" {
				cleanupErr = cleanupDeviceAudits(context.Background(), setup, []string{request})
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
	if err := setup.Table("station").Create(map[string]any{"id": id, "name": board, "longitude": 0, "latitude": 0, "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
	firstBefore, secondBefore, inserted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	firstGo, secondGo, commitGo := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var onceFirst, onceSecond, onceInserted, passFirst, passSecond, passCommit sync.Once
	releaseFirst := func() { passFirst.Do(func() { close(firstGo) }) }
	releaseSecond := func() { passSecond.Do(func() { close(secondGo) }) }
	releaseCommit := func() { passCommit.Do(func() { close(commitGo) }) }
	defer releaseFirst()
	defer releaseSecond()
	defer releaseCommit()
	key := "import_claim_" + uuid.NewString()
	match := func(q *gorm.DB) bool {
		return strings.HasPrefix(q.Statement.SQL.String(), "INSERT IGNORE INTO device_import(")
	}
	if err := first.Callback().Raw().Before("gorm:raw").Register(key, func(q *gorm.DB) {
		if match(q) {
			onceFirst.Do(func() { close(firstBefore); <-firstGo })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer first.Callback().Raw().Remove(key)
	if err := first.Callback().Raw().After("gorm:raw").Register(key+"_inserted", func(q *gorm.DB) {
		if match(q) && q.Error == nil {
			onceInserted.Do(func() { close(inserted); <-commitGo })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer first.Callback().Raw().Remove(key + "_inserted")
	if err := second.Callback().Raw().Before("gorm:raw").Register(key, func(q *gorm.DB) {
		if match(q) {
			onceSecond.Do(func() { close(secondBefore); <-secondGo })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer second.Callback().Raw().Remove(key)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gateway := &concurrentImportGateway{vendor: id}
	audit := ops.AuditContext{Actor: operations.Profile{ID: id, Permissions: []string{"device.import"}}, RequestID: request}
	scope := operations.DataScope{Unrestricted: true}
	originalModel := "原型号"
	in := device.ImportInput{ImportID: request, Devices: []device.ImportDevice{{DeviceID: board, VendorID: id, StationID: id, PortCount: 2, Model: &originalModel}}}
	type answer struct {
		out device.ImportJob /* 原任务状态 */
		err error            /* 原申请结果 */
	}
	call := func(db *gorm.DB) chan answer {
		ch := make(chan answer, 1)
		go func() {
			out, err := (device.ImportService{DB: db, Gateway: gateway, Compatibility: pricing.Repo{DB: db}, LoadScope: func(context.Context, operations.Profile) (operations.DataScope, error) { return scope, nil }}).Create(ctx, audit, scope, in)
			ch <- answer{out, err}
		}()
		return ch
	}
	a := call(first)
	select {
	case <-firstBefore:
	case <-ctx.Done():
		t.Fatal("first import insert not reached")
	}
	b := call(second)
	select {
	case <-secondBefore:
	case <-ctx.Done():
		t.Fatal("second import insert not attempted")
	}
	releaseFirst()
	select {
	case <-inserted:
	case <-ctx.Done():
		t.Fatal("first claim blocked before registration; possible missing-row gap lock cycle")
	}
	releaseSecond()
	releaseCommit()
	one, two := <-a, <-b
	if one.err != nil || two.err != nil || one.out.ImportID != request || two.out.ImportID != request || one.out.Status != "completed" || two.out.Status != "completed" || gateway.calls.Load() != 1 {
		t.Fatalf("first=%+v second=%+v calls=%d", one, two, gateway.calls.Load())
	}
	for _, q := range []struct {
		table string /* 原持久表 */
		where string /* 身份筛选 */
		arg   any    /* 唯一测试身份 */
		want  int64  /* 应提交行数 */
	}{
		{"device_import", "import_id=?", request, 1}, {"device_import_identity", "device_id=?", board, 1}, {"device_meta", "device_id=?", board, 1}, {"audit_log", "request_id=?", request, 2},
	} {
		var n int64
		if err := setup.Table(q.table).Where(q.where, q.arg).Count(&n).Error; err != nil || n != q.want {
			t.Errorf("%s=%d want=%d %v", q.table, n, q.want, err)
		}
	}
	changed := in
	changed.Devices = append([]device.ImportDevice(nil), in.Devices...)
	changedModel := "篡改型号"
	changed.Devices[0].Model = &changedModel
	svc := device.ImportService{DB: setup, Gateway: gateway, Compatibility: pricing.Repo{DB: setup}, LoadScope: func(context.Context, operations.Profile) (operations.DataScope, error) { return scope, nil }}
	if _, err := svc.Create(ctx, audit, scope, changed); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("changed replay=%v", err)
	}
	audit.Actor.ID++
	if _, err := svc.Create(ctx, audit, scope, in); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("different actor replay=%v", err)
	}
	if gateway.calls.Load() != 1 {
		t.Fatalf("conflict replay called gateway: %d", gateway.calls.Load())
	}
}
