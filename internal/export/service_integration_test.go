//go:build integration

package export

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/testfixture"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// exportFixture 用提交的私有账号／角色／站点验证恢复上下文，持续限定源查询为自身身份。
// 清理只删除本fixture引用和任务，不接触bootstrap、开发库、资金或共享范围。
func exportFixture(t *testing.T) (ExportService, ops.AuditContext, uint64, uint64) {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_URL")
	if raw == "" {
		t.Fatal("TEST_MYSQL_URL required")
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
	id := testfixture.NewID()
	name := "export_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	t.Cleanup(func() {
		if err := cleanupExportAudits(db, id); err != nil {
			t.Error(err)
		}
		for _, query := range []struct {
			table, where string // 自有数据的表与删除条件
			arg          any    // fixture精确身份
		}{
			{"export_task", "requested_by=?", id}, {"admin_data_scope", "admin_user_id=?", id}, {"admin_field_mask", "role_id=?", id}, {"role_permission", "role_id=?", id}, {"admin_user", "id=?", id}, {"role", "id=?", id},
		} {
			if err := db.Table(query.table).Where(query.where, query.arg).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
		}
		if err := db.Exec("DELETE FROM station WHERE id IN (?,?)", id, id+1).Error; err != nil {
			t.Error(err)
		}
	})
	for _, seed := range []struct {
		table string         // fixture目标表
		row   map[string]any // 自有身份及必需字段
	}{
		{"role", map[string]any{"id": id, "code": name, "name": name, "is_builtin": false}},
		{"admin_user", map[string]any{"id": id, "username": name, "password_hash": "export_private_fixture", "role_id": id, "status": "active"}},
		{"station", map[string]any{"id": id, "name": "中文站点一", "longitude": 0, "latitude": 0}},
		{"station", map[string]any{"id": id + 1, "name": "中文站点二", "longitude": 0, "latitude": 0}},
	} {
		if err := db.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(err)
		}
	}

	for _, code := range []string{"export.create", "finance.read", "station.read", "device.read", "order.read", "admin_user.read"} {
		if err := db.Exec("INSERT INTO role_permission(role_id,permission_id) SELECT ?,id FROM permission WHERE code=?", id, code).Error; err != nil {
			t.Fatal(err)
		}
	}
	profile, err := (ops.AccountRepo{DB: db}).Profile(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement.Table == "station" {
			q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.IN{Column: "id", Values: []any{id, id + 1}}}})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(name) })
	return ExportService{DB: db, ExportDir: t.TempDir(), MaxRows: 2}, ops.AuditContext{Actor: profile, RequestID: name}, id, id + 1
}

// TestExportPublishDownloadAndCurrentScope 验证真实同步发布、重启可读凭据、范围／mask收窄、路径及原回执。
func TestExportPublishDownloadAndCurrentScope(t *testing.T) {
	s, audit, first, _ := exportFixture(t)
	ctx := context.Background()
	in := CreateInput{RequestID: uuid.NewString(), Resource: "stations"}
	result, err := s.Create(ctx, audit, in)
	if err != nil || result.Status != "completed" || result.RowCount != 2 {
		t.Fatalf("create %+v %v", result, err)
	}
	row, err := s.repo().FindExportTaskByNo(s.DB, result.TaskNo)
	if err != nil {
		t.Fatal(err)
	}
	if row.FilterJSON == nil || *row.FilterJSON != "null" || row.ExpiresAt == nil {
		t.Fatalf("original task %+v", row)
	}
	if _, err := time.Parse(time.RFC3339Nano, *row.ExpiresAt); err != nil {
		t.Fatalf("driver expiry shape %q %v", *row.ExpiresAt, err)
	}
	// 新服务实例完全从持久数据库及文件验证，不依赖生成时内存。
	restarted := ExportService{DB: s.DB, ExportDir: s.ExportDir, MaxRows: 2}
	download, err := restarted.GetDownload(ctx, audit.Actor, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(download.File)
	closeErr := download.File.Close()
	if err != nil || closeErr != nil || !strings.Contains(string(body), "中文站点一") || !strings.Contains(string(body), "名称,地址,状态") {
		t.Fatalf("CSV %q %v %v", body, err, closeErr)
	}
	replayed, err := s.Create(ctx, audit, in)
	if err != nil || !reflect.DeepEqual(replayed, result) {
		t.Fatalf("replay %+v %v", replayed, err)
	}
	in.Filter = map[string]any{}
	if _, err := s.Create(ctx, audit, in); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("null/empty filter accepted %v", err)
	}
	var audits int64
	if err := s.DB.Table("audit_log").Where("actor_id=? AND target_type='export_task'", audit.Actor.ID).Count(&audits).Error; err != nil || audits != 1 {
		t.Fatalf("audit %d %v", audits, err)
	}
	if err := s.DB.Exec("INSERT INTO admin_data_scope(admin_user_id,scope_type,scope_id,created_by) VALUES (?,'station',?,?)", audit.Actor.ID, first, audit.Actor.ID).Error; err != nil {
		t.Fatal(err)
	}
	var forbidden *ForbiddenError
	if _, err := restarted.GetDownload(ctx, audit.Actor, row.ID); !errors.As(err, &forbidden) {
		t.Fatalf("scope shrink accepted %v", err)
	}
	if err := s.DB.Exec("DELETE FROM admin_data_scope WHERE admin_user_id=?", audit.Actor.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("INSERT INTO admin_field_mask(role_id,resource,field) VALUES (?,'station','name')", audit.Actor.RoleID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.GetDownload(ctx, audit.Actor, row.ID); !errors.As(err, &forbidden) {
		t.Fatalf("mask shrink accepted %v", err)
	}
	if err := s.DB.Exec("DELETE FROM admin_field_mask WHERE role_id=?", audit.Actor.RoleID).Error; err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Table("export_task").Where("id=?", row.ID).Update("file_path", outside).Error; err != nil {
		t.Fatal(err)
	}
	var state *DownloadError
	if _, err := restarted.GetDownload(ctx, audit.Actor, row.ID); !errors.As(err, &state) {
		t.Fatalf("outside path accepted %v", err)
	}
	if err := s.DB.Table("export_task").Where("id=?", row.ID).Update("expires_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.GetDownload(ctx, audit.Actor, row.ID); !errors.As(err, &state) || !state.Expired {
		t.Fatalf("expired accepted %v", err)
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "preserve" {
		t.Fatalf("outside removed %v", err)
	}
	for _, suffix := range []string{".csv", ".scope.json"} {
		if _, err := os.Stat(filepath.Join(s.ExportDir, "v1", row.TaskNo+suffix)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expired artifact %s %v", suffix, err)
		}
	}
	if removed, err := s.CleanupExpired(ctx); err != nil || removed != 0 {
		t.Fatalf("repeat cleanup %d %v", removed, err)
	}
}

// TestExportFailureRollbackAndPendingRecovery 验证审计回滚、超限、IO失败、取消及原pending同步恢复。
func TestExportFailureRollbackAndPendingRecovery(t *testing.T) {
	s, audit, _, _ := exportFixture(t)
	ctx := context.Background()
	in := CreateInput{RequestID: uuid.NewString(), Resource: "stations"}
	fault := errors.New("private export audit failure")
	key := "export-audit-fault"
	if err := s.DB.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "audit_log" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Create(ctx, audit, in)
	s.DB.Callback().Create().Remove(key)
	if !errors.Is(err, fault) {
		t.Fatalf("audit %v", err)
	}
	var count int64
	if err := s.DB.Table("export_task").Where("requested_by=?", audit.Actor.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("partial %d %v", count, err)
	}
	s.MaxRows = 1
	result, err := s.Create(ctx, audit, in)
	if err != nil || result.Status != "failed" || result.Message == "" {
		t.Fatalf("overflow %+v %v", result, err)
	}
	row, err := s.repo().FindExportTaskByNo(s.DB, result.TaskNo)
	if err != nil || row.Status != "failed" {
		t.Fatalf("persist overflow %+v %v", row, err)
	}
	if entries, err := os.ReadDir(s.ExportDir); err != nil || len(entries) != 0 {
		t.Fatalf("overflow files %v %v", entries, err)
	}
	// 已受理pending保留完整原UUID；重启后同申请恢复同步生成且不重复审计。
	s.MaxRows = 2
	in.RequestID = uuid.NewString()
	taskNo := "EXP" + strings.ToUpper(strings.ReplaceAll(in.RequestID, "-", ""))
	_, err = s.repo().ClaimExportTask(s.DB, ExportTaskInsert{TaskNo: taskNo, Resource: "stations", FileFormat: "csv", RequestedBy: audit.Actor.ID, FilterJSON: "null", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.Create(ctx, audit, in)
	if err != nil || result.Status != "completed" {
		t.Fatalf("pending recovery %+v %v", result, err)
	}
	// 在实际取数返回后取消，失败状态用同步有界恢复context持久，不生成后续字节。
	in.RequestID = uuid.NewString()
	cancelled, cancel := context.WithCancel(ctx)
	defer cancel()
	key = "export-cancel-source"
	if err := s.DB.Callback().Query().After("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "station" {
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	result, err = s.Create(cancelled, audit, in)
	s.DB.Callback().Query().Remove(key)
	if err != nil || result.Status != "failed" {
		t.Fatalf("cancelled %+v %v", result, err)
	}
	row, err = s.repo().FindExportTaskByNo(s.DB, result.TaskNo)
	if err != nil || row.Status != "failed" {
		t.Fatalf("cancel state %+v %v", row, err)
	}
	for _, suffix := range []string{".csv", ".scope.json", ".csv.partial", ".scope.json.partial"} {
		if _, err := os.Stat(filepath.Join(s.ExportDir, "v1", result.TaskNo+suffix)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancel artifact %s %v", suffix, err)
		}
	}
	// 配置目录为已存在普通文件，IO失败应持久失败，原文件保持。
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.ExportDir = blocked
	in.RequestID = uuid.NewString()
	result, err = s.Create(ctx, audit, in)
	if err != nil || result.Status != "failed" {
		t.Fatalf("IO %+v %v", result, err)
	}
	if raw, err := os.ReadFile(blocked); err != nil || string(raw) != "keep" {
		t.Fatalf("IO changed existing file %v", err)
	}
}

// TestExportPrivateManifestIntegrity 验证持久文件摘要及任务身份失配，不能因completed直接放行。
func TestExportPrivateManifestIntegrity(t *testing.T) {
	s, audit, _, _ := exportFixture(t)
	ctx := context.Background()
	result, err := s.Create(ctx, audit, CreateInput{RequestID: uuid.NewString(), Resource: "stations"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.repo().FindExportTaskByNo(s.DB, result.TaskNo)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.ExportDir, "v1", row.TaskNo+".scope.json")
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var missing *DownloadError
	if _, err := s.GetDownload(ctx, audit.Actor, row.ID); !errors.As(err, &missing) {
		t.Fatalf("missing new proof accepted %v", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	var proof artifactProof
	if err := json.Unmarshal(encoded, &proof); err != nil {
		t.Fatal(err)
	}
	proof.RequestedBy++
	encoded, err = json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	var state *DownloadError
	if _, err := s.GetDownload(ctx, audit.Actor, row.ID); !errors.As(err, &state) {
		t.Fatalf("wrong identity %v", err)
	}
	proof.RequestedBy--
	encoded, _ = json.Marshal(proof)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(*row.FilePath, []byte("different bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDownload(ctx, audit.Actor, row.ID); !errors.As(err, &state) {
		t.Fatalf("wrong digest %v", err)
	}
}
