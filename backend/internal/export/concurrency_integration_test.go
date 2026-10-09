//go:build integration

package export

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chargepilot/internal/ops"
	"chargepilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// secondExportDB 创建独立连接池，源查询只覆盖本测试两个站点，结束后关闭池和回调。
func secondExportDB(t *testing.T, first, second uint64) *gorm.DB {
	t.Helper()
	pool, err := dbconn.Open(t.Context(), os.Getenv("TEST_MYSQL_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	db, err := dbconn.WrapGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	key := "export-second-" + uuid.NewString()
	if err := db.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "station" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.IN{Column: "id", Values: []any{first, second}}}})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(key) })
	return db
}

// awaitExportSignal 等待真实数据库步骤通知，超时终止测试，不用睡眠假定已取锁。
func awaitExportSignal(t *testing.T, ctx context.Context, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// TestExportConcurrentClaimAndOldSnapshot 验证同 UUID 并发重放，以及旧 RR 快照下锁读最新任务。
func TestExportConcurrentClaimAndOldSnapshot(t *testing.T) {
	s, audit, first, second := exportFixture(t)
	other := s
	other.DB = secondExportDB(t, first, second)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	in := CreateInput{RequestID: uuid.NewString(), Resource: "stations"}
	entered, release, attempted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var rawOnce, queryOnce, releaseOnce sync.Once
	key := "export-claim-gate"
	if err := s.DB.Callback().Raw().After("gorm:raw").Register(key, func(q *gorm.DB) {
		if strings.HasPrefix(q.Statement.SQL.String(), "INSERT INTO export_task(") {
			rawOnce.Do(func() {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					q.AddError(ctx.Err())
				}
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Callback().Raw().Remove(key)
	if err := other.DB.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "role" {
			queryOnce.Do(func() { close(attempted) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer other.DB.Callback().Query().Remove(key)
	// outcome 汇总同一申请的两次创建结果，任务和审计数量另查真库。
	type outcome struct {
		result CreateResult // 同步回执
		err    error        // 事务或校验错误
	}
	results := make(chan outcome, 2)
	var workers sync.WaitGroup
	defer func() { cancel(); releaseOnce.Do(func() { close(release) }); workers.Wait() }()
	workers.Go(func() { out, err := s.Create(ctx, audit, in); results <- outcome{out, err} })
	awaitExportSignal(t, ctx, entered)
	workers.Go(func() { out, err := other.Create(ctx, audit, in); results <- outcome{out, err} })
	awaitExportSignal(t, ctx, attempted)
	select {
	case result := <-results:
		t.Fatalf("claim escaped held transaction: %+v", result)
	default:
	}
	releaseOnce.Do(func() { close(release) })
	var outcomes []outcome
	for range 2 {
		select {
		case out := <-results:
			outcomes = append(outcomes, out)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	for _, out := range outcomes {
		if out.err != nil || out.result.Status != "completed" || out.result.RowCount != 2 {
			t.Fatalf("claim %+v", out)
		}
	}
	if !reflect.DeepEqual(outcomes[0].result, outcomes[1].result) {
		t.Fatalf("replay drift: %+v", outcomes)
	}
	var audits int64
	if err := s.DB.Table("audit_log").Where("actor_id=? AND target_type='export_task'", audit.Actor.ID).Count(&audits).Error; err != nil || audits != 1 {
		t.Fatalf("audits %d %v", audits, err)
	}
	// 普通查询保留旧 RR 快照，锁读必须看到另一连接已提交的 completed 任务并重放。
	old := other.DB.WithContext(ctx).Begin()
	if old.Error != nil {
		t.Fatal(old.Error)
	}
	defer old.Rollback()
	in.RequestID = uuid.NewString()
	taskNo := "EXP" + strings.ToUpper(strings.ReplaceAll(in.RequestID, "-", ""))
	var count int64
	if err := old.Table("export_task").Where("task_no=?", taskNo).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("initial snapshot %d %v", count, err)
	}
	original, err := s.Create(ctx, audit, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Table("export_task").Where("task_no=?", taskNo).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("not old RR %d %v", count, err)
	}
	stale := other
	stale.DB = old
	replay, err := stale.Create(ctx, audit, in)
	if err != nil || !reflect.DeepEqual(replay, original) {
		t.Fatalf("old replay %+v original %+v %v", replay, original, err)
	}
	row, err := s.repo().FindExportTaskByNo(s.DB, original.TaskNo)
	if err != nil {
		t.Fatal(err)
	}
	download, err := stale.GetDownload(ctx, audit.Actor, row.ID)
	if err != nil {
		t.Fatalf("old RR expiry current read: %v", err)
	}
	if err := download.File.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestExportClockAfterLockAndPublication 验证取锁或取数后已过期时，不能下载或标为 completed。
func TestExportClockAfterLockAndPublication(t *testing.T) {
	s, audit, first, second := exportFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var clock atomic.Int64
	start := time.Now().UTC().Truncate(time.Second)
	clock.Store(start.UnixNano())
	s.Clock = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	result, err := s.Create(ctx, audit, CreateInput{RequestID: uuid.NewString(), Resource: "stations"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.repo().FindExportTaskByNo(s.DB, result.TaskNo)
	if err != nil {
		t.Fatal(err)
	}
	blocking := secondExportDB(t, first, second).WithContext(ctx).Begin()
	if blocking.Error != nil {
		t.Fatal(blocking.Error)
	}
	defer blocking.Rollback()
	if err := (ops.AccountRepo{DB: blocking}).LockCustomerAdminRole(blocking); err != nil {
		t.Fatal(err)
	}
	attempted := make(chan struct{})
	var once sync.Once
	key := "export-clock-wait"
	if err := s.DB.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "role" {
			once.Do(func() { close(attempted) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Callback().Query().Remove(key)
	finished := make(chan error, 1)
	var workers sync.WaitGroup
	defer func() { cancel(); blocking.Rollback(); workers.Wait() }()
	workers.Go(func() {
		out, err := s.GetDownload(ctx, audit.Actor, row.ID)
		if out.File != nil {
			out.File.Close()
		}
		finished <- err
	})
	awaitExportSignal(t, ctx, attempted)
	clock.Store(start.Add(25 * time.Hour).UnixNano())
	if err := blocking.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var expired *DownloadError
	select {
	case err := <-finished:
		if !errors.As(err, &expired) || !expired.Expired {
			t.Fatalf("after-lock expiry %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	s.DB.Callback().Query().Remove(key)
	clock.Store(start.UnixNano())
	key = "export-clock-source"
	if err := s.DB.Callback().Query().After("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "station" {
			clock.Store(start.Add(25 * time.Hour).UnixNano())
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Callback().Query().Remove(key)
	result, err = s.Create(ctx, audit, CreateInput{RequestID: uuid.NewString(), Resource: "stations"})
	if err != nil || result.Status != "failed" {
		t.Fatalf("publication expiry %+v %v", result, err)
	}
	row, err = s.repo().FindExportTaskByNo(s.DB, result.TaskNo)
	if err != nil || row.Status != "failed" || row.FilePath != nil {
		t.Fatalf("expiry truth %+v %v", row, err)
	}
	for _, suffix := range []string{".csv", ".scope.json", ".csv.partial", ".scope.json.partial"} {
		if _, err := os.Stat(filepath.Join(s.ExportDir, "v1", row.TaskNo+suffix)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expiry artifact %s %v", suffix, err)
		}
	}
}
