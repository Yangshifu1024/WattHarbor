//go:build integration

package export

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestExportSQLFailureAndHistoricalFiles 验证完成写失败后的文件恢复、失败状态写失败，以及旧文件下载条件。
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
		// Dest 取值是 any，值是具名类型 ExportStatus 时与 string 字面量比较恒为 false，
		// 因此按值文本比较而不是按动态类型比较。词表见 ExportStatus。
		if ok && fmt.Sprint(values["status"]) == status {
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
	// 失败状态写入也失败时返回内部错误并保留 pending，不能返回 failed。
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
	// 旧根目录文件缺少校验文件时，仅全局且无需脱敏的账号可下载；v1 文件必须有校验文件。
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

// TestHistoricalPDFUnavailable 验证历史 PDF 仅按可信任务号清理，不提供下载；不修改测试库格式枚举。
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
