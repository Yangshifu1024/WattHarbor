//go:build integration

package export

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestExportCancellationKeepsLockUntilCallbackExit 验证文件发布后取消请求，任务锁仍保持到同步回调退出。
// 两池 SQL 和文件验证真实执行，不测试磁盘写入中断。
func TestExportCancellationKeepsLockUntilCallbackExit(t *testing.T) {
	s, audit, first, second := exportFixture(t)
	other := s
	other.DB = secondExportDB(t, first, second)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	firstCtx, cancelFirst := context.WithCancel(ctx)
	in := CreateInput{RequestID: uuid.NewString(), Resource: "stations"}
	taskNo := "EXP" + strings.ToUpper(strings.ReplaceAll(in.RequestID, "-", ""))
	entered, release, attempted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce, callbackOnce, attemptOnce sync.Once
	key := "export-cancellation-gate"
	if err := s.DB.Callback().Update().Before("gorm:update").Register(key, func(q *gorm.DB) {
		values, ok := q.Statement.Dest.(map[string]any)
		if q.Statement.Table == "export_task" && ok && values["status"] == "completed" {
			callbackOnce.Do(func() {
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
	defer s.DB.Callback().Update().Remove(key)
	if err := other.DB.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "role" {
			attemptOnce.Do(func() { close(attempted) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer other.DB.Callback().Query().Remove(key)
	// outcome 汇总取消和竞争导出结果，最终任务状态和文件另行核对。
	type outcome struct {
		result CreateResult // 同步回执
		err    error        // 取消或事务错误
	}
	firstResult, secondResult := make(chan outcome, 1), make(chan outcome, 1)
	var workers sync.WaitGroup
	defer func() { cancelFirst(); cancel(); releaseOnce.Do(func() { close(release) }); workers.Wait() }()
	workers.Go(func() { out, err := s.Create(firstCtx, audit, in); firstResult <- outcome{out, err} })
	awaitExportSignal(t, ctx, entered)
	for _, suffix := range []string{".csv", ".scope.json"} {
		if _, err := os.Stat(filepath.Join(s.ExportDir, "v1", taskNo+suffix)); err != nil {
			t.Fatalf("not published %s %v", suffix, err)
		}
	}
	cancelFirst()
	workers.Go(func() { out, err := other.Create(ctx, audit, in); secondResult <- outcome{out, err} })
	awaitExportSignal(t, ctx, attempted)
	// 让 database/sql 有机会执行取消回滚，再用第二个真实请求是否完成判断锁是否已释放。
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case out := <-secondResult:
		t.Fatalf("lock released before callback exit %+v", out)
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	releaseOnce.Do(func() { close(release) })
	var firstOut, secondOut outcome
	select {
	case firstOut = <-firstResult:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case secondOut = <-secondResult:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if firstOut.err != nil && !errors.Is(firstOut.err, context.Canceled) {
		t.Fatalf("first outcome %+v", firstOut)
	}
	if firstOut.err == nil && firstOut.result.Status != "failed" {
		t.Fatalf("cancel reported success %+v", firstOut)
	}
	if secondOut.err != nil || (secondOut.result.Status != "completed" && secondOut.result.Status != "failed") {
		t.Fatalf("second outcome %+v", secondOut)
	}
	row, err := s.repo().FindExportTaskByNo(s.DB, taskNo)
	if err != nil || row.Status != secondOut.result.Status {
		t.Fatalf("final truth %+v %v", row, err)
	}
	if row.Status == "completed" {
		out, err := s.GetDownload(ctx, audit.Actor, row.ID)
		if err != nil {
			t.Fatal(err)
		}
		raw, readErr := io.ReadAll(out.File)
		closeErr := out.File.Close()
		if readErr != nil || closeErr != nil || !strings.Contains(string(raw), "中文站点一") || !strings.Contains(string(raw), "中文站点二") {
			t.Fatalf("competitor corrupted %q %v %v", raw, readErr, closeErr)
		}
	} else {
		for _, suffix := range []string{".csv", ".scope.json", ".csv.partial", ".scope.json.partial"} {
			if _, err := os.Stat(filepath.Join(s.ExportDir, "v1", taskNo+suffix)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed artifact %s %v", suffix, err)
			}
		}
	}
}
