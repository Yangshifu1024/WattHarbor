//go:build integration

package export

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestExportSQLFailureAndHistoricalFiles 验证完成写失败的文件恢复、失败写不可用时不误报，以及真正旧文件准入。
func TestExportSQLFailureAndHistoricalFiles(t *testing.T) {
	s, audit, first, _ := exportFixture(t)
	ctx := context.Background()
	fault := errors.New("private export state failure")
	key := "export-state-fault"
	status := "completed"
	if err := s.DB.Callback().Update().Before("gorm:update").Register(key, func(q *gorm.DB) {
		if q.Statement.Table != "export_task" {
			return
		}
		values, ok := q.Statement.Dest.(map[string]any)
		if ok && values["status"] == status {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Callback().Update().Remove(key)
	in := CreateInput{RequestID: uuid.NewString(), Resource: "stations"}
	result, err := s.Create(ctx, audit, in)
	if err != nil || result.Status != "failed" {
		t.Fatalf("complete fault %+v %v", result, err)
	}
	row, err := s.repo().FindExportTaskByNo(s.DB, result.TaskNo)
	if err != nil || row.Status != "failed" || row.FilePath != nil {
		t.Fatalf("fault state %+v %v", row, err)
	}
	for _, suffix := range []string{".csv", ".scope.json", ".csv.partial", ".scope.json.partial"} {
		if _, err := os.Stat(filepath.Join(s.ExportDir, "v1", row.TaskNo+suffix)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("fault artifact %s %v", suffix, err)
		}
	}
	// 超限之后的失败写也故障，必须返回内部错误并保留pending，不能返回伪failed回执。
	status = "failed"
	s.MaxRows = 1
	in.RequestID = uuid.NewString()
	_, err = s.Create(ctx, audit, in)
	if !errors.Is(err, fault) {
		t.Fatalf("failure persistence swallowed %v", err)
	}
	taskNo := "EXP" + strings.ToUpper(strings.ReplaceAll(in.RequestID, "-", ""))
	row, err = s.repo().FindExportTaskByNo(s.DB, taskNo)
	if err != nil || row.Status != "pending" {
		t.Fatalf("unconfirmed truth %+v %v", row, err)
	}
	s.DB.Callback().Update().Remove(key)
	s.MaxRows = 2
	result, err = s.Create(ctx, audit, in)
	if err != nil || result.Status != "completed" {
		t.Fatalf("retry persisted pending %+v %v", result, err)
	}
	// 历史根目录文件无sidecar，仅当前全局且无需脱敏时兼容；不把新v1文件降级为历史。
	oldNo := "EXP" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))
	legacy := filepath.Join(s.ExportDir, oldNo+".csv")
	if err := os.WriteFile(legacy, []byte("历史内容\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.repo().ClaimExportTask(s.DB, ExportTaskInsert{TaskNo: oldNo, Resource: "stations", FileFormat: "csv", RequestedBy: audit.Actor.ID, FilterJSON: "null", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := s.repo().LockExportTaskByNo(tx, oldNo); err != nil {
			return err
		}
		return s.repo().CompleteInTx(tx, oldNo, 1, legacy, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	row, err = s.repo().FindExportTaskByNo(s.DB, oldNo)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.GetDownload(ctx, audit.Actor, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, readErr := io.ReadAll(out.File)
	closeErr := out.File.Close()
	if string(raw) != "历史内容\n" || readErr != nil || closeErr != nil {
		t.Fatalf("legacy %q %v %v", raw, readErr, closeErr)
	}
	if err := s.DB.Exec("INSERT INTO admin_data_scope(admin_user_id,scope_type,scope_id,created_by) VALUES (?,'station',?,?)", audit.Actor.ID, first, audit.Actor.ID).Error; err != nil {
		t.Fatal(err)
	}
	var denied *ForbiddenError
	if _, err := s.GetDownload(ctx, audit.Actor, row.ID); !errors.As(err, &denied) {
		t.Fatalf("legacy scope bypass %v", err)
	}
	if err := s.DB.Exec("UPDATE export_task SET expires_at=? WHERE id=?", time.Now().Add(-time.Hour), row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(legacy); err != nil {
		t.Fatal(err)
	}
	removed, err := s.CleanupExpired(ctx)
	if err != nil || removed != 1 {
		t.Fatalf("missing legacy cleanup %d %v", removed, err)
	}
	if removed, err := s.CleanupExpired(ctx); err != nil || removed != 0 {
		t.Fatalf("repeat missing cleanup %d %v", removed, err)
	}
}

// TestHistoricalPDFUnavailable 验证历史PDF仅可按可信任务身份清理，不对当前下载开放。
// 当前测试库枚举只有CSV／XLSX，不为测试改schema或伪造数据库支持PDF的事实。
func TestHistoricalPDFUnavailable(t *testing.T) {
	row := ExportTaskRow{TaskNo: "EXPABC123", FileFormat: "pdf"}
	var state *DownloadError
	if _, _, err := fileNames(row, false); !errors.As(err, &state) {
		t.Fatalf("PDF download %v", err)
	}
	name, _, err := fileNames(row, true)
	if err != nil || name != "EXPABC123.pdf" {
		t.Fatalf("PDF cleanup %q %v", name, err)
	}
}
