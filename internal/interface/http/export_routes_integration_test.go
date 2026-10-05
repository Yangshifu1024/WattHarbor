//go:build integration

package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	exportpkg "github.com/ChargePilot2026/charge-pilot/internal/export"
	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	adminexport "github.com/ChargePilot2026/charge-pilot/internal/interface/http/admin/export"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestRegisteredExportRoutes 用实际注册路由及SQL／Redis验证原CSV／XLSX回执、下载、错误和实时撤权。
// 私有角色、账号、站点、审计及任务在外层事务回滚，文件归测试临时目录，随机会话退出注销。
func TestRegisteredExportRoutes(t *testing.T) {
	tx := cardRouteDB(t, "TEST_MYSQL_URL").Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	ctx := context.Background()
	cache := identityRouteRedis(t)
	id := uint64(time.Now().UnixNano())
	tag := "export-route-" + uuid.NewString()
	for _, seed := range []struct {
		table string         // fixture所属表
		row   map[string]any // 自有身份和必需字段
	}{
		{"role", map[string]any{"id": id + 1, "code": tag, "name": "导出注册操作者", "is_builtin": false}},
		{"admin_user", map[string]any{"id": id, "username": tag, "password_hash": "export_route_private_hash", "role_id": id + 1, "status": "active", "auth_version": 9007199254741133}},
		{"station", map[string]any{"id": id, "name": "注册中文站点", "longitude": 0, "latitude": 0}},
		{"admin_data_scope", map[string]any{"admin_user_id": id, "scope_type": "station", "scope_id": id, "created_by": id}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, code := range []string{"export.create", "finance.read", "station.read"} {
		if err := tx.Exec("INSERT INTO role_permission(role_id,permission_id) SELECT ?,id FROM permission WHERE code=?", id+1, code).Error; err != nil {
			t.Fatal(err)
		}
	}
	profile, err := (ops.AccountRepo{DB: tx}).Profile(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	sessions := ops.SessionRepo{Redis: cache}
	sid, _, err := sessions.Create(ctx, operations.AccountIdentity{ID: id, Username: profile.Username, AuthVersion: profile.AuthVersion})
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.RevokeSession(ctx, sid)
	jwt, err := auth.NewJWT(strings.Repeat("export-route-key-", 3))
	if err != nil {
		t.Fatal(err)
	}
	token, err := (ops.JWTAccess{JWT: jwt}).Sign(profile, sid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	authAPI := chargehttp.AdminAuth{Sessions: ops.SessionService{Accounts: ops.AccountRepo{DB: tx}, Sessions: sessions, Tokens: ops.JWTAccess{JWT: jwt}}}
	router := httpapi.NewRouter()
	service := exportpkg.ExportService{DB: tx, ExportDir: t.TempDir(), MaxRows: 2}
	(adminexport.ExportTask{Service: service, Auth: authAPI}).Register(router)
	base := "/api/v1/admin/exports"
	call := func(method, path string, input any, want int) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", tag)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s %d want%d body%s", method, path, w.Code, want, w.Body)
		}
		if strings.Contains(w.Body.String(), service.ExportDir) || strings.Contains(w.Body.String(), "export_route_private_hash") {
			t.Fatal("private export material leaked")
		}
		return w
	}
	resources := call("GET", base+"/resources", nil, 200)
	if !strings.Contains(resources.Body.String(), `"formats":["csv","xlsx"]`) || strings.Contains(resources.Body.String(), `"bills"`) {
		t.Fatalf("resources %s", resources.Body)
	}
	call("GET", base+"?page_size=0", nil, 400)
	call("GET", base+"?status=unknown", nil, 400)
	call("POST", base, map[string]any{"request_id": uuid.NewString(), "resource": "stations", "unknown": true}, 400)
	for _, format := range []string{"pdf", "unknown"} {
		call("POST", base, map[string]any{"request_id": uuid.NewString(), "resource": "stations", "format": format}, 400)
	}
	var lastID uint64
	for _, format := range []string{"csv", "xlsx"} {
		in := map[string]any{"request_id": uuid.NewString(), "resource": "stations", "format": format}
		created := call("POST", base, in, 200)
		var envelope struct {
			Data struct {
				TaskNo    string  `json:"task_no"`    // 完整原任务身份
				Status    string  `json:"status"`     // 原同步结果
				RowCount  uint32  `json:"row_count"`  // 原实际行数
				ExpiresAt *string `json:"expires_at"` // 原可空截止字符串
			} `json:"data"` // 原响应包络
		}
		if err := json.Unmarshal(created.Body.Bytes(), &envelope); err != nil || envelope.Data.Status != "completed" || envelope.Data.RowCount != 1 || envelope.Data.ExpiresAt == nil {
			t.Fatalf("create %s %v", created.Body, err)
		}
		if replay := call("POST", base, in, 200); replay.Body.String() != created.Body.String() {
			t.Fatalf("replay %s/%s", created.Body, replay.Body)
		}
		row, err := (exportpkg.ExportRepo{DB: tx}).FindExportTaskByNo(tx, envelope.Data.TaskNo)
		if err != nil {
			t.Fatal(err)
		}
		lastID = row.ID
		path := fmt.Sprintf("%s/%d", base, row.ID)
		call("GET", path, nil, 200)
		w := call("GET", path+"/download", nil, 200)
		if !strings.Contains(w.Header().Get("Content-Disposition"), row.TaskNo+"."+format) {
			t.Fatalf("name %v", w.Header())
		}
		if format == "csv" {
			if w.Header().Get("Content-Type") != "text/csv; charset=utf-8" || !strings.Contains(w.Body.String(), "注册中文站点") {
				t.Fatalf("CSV %v %s", w.Header(), w.Body)
			}
		} else if w.Header().Get("Content-Type") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" || !strings.HasPrefix(w.Body.String(), "PK") {
			t.Fatalf("XLSX %v", w.Header())
		}
	}
	call("GET", base, nil, 200)
	call("GET", base+"/18446744073709551615", nil, 404)
	// 首次审计写故障整次受理回滚为503，不暴露内部存储原因。
	fault := errors.New("registered export private audit failure")
	key := "registered-export-fault-" + tag
	if err := tx.Callback().Create().Before("gorm:create").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "audit_log" {
			q.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Callback().Create().Remove(key) })
	failed := call("POST", base, map[string]any{"request_id": uuid.NewString(), "resource": "stations"}, 503)
	if strings.Contains(failed.Body.String(), fault.Error()) {
		t.Fatal("storage reason leaked")
	}
	tx.Callback().Create().Remove(key)
	if err := tx.Exec("DELETE rp FROM role_permission rp JOIN permission p ON p.id=rp.permission_id WHERE rp.role_id=? AND p.code='station.read'", id+1).Error; err != nil {
		t.Fatal(err)
	}
	call("GET", fmt.Sprintf("%s/%d/download", base, lastID), nil, 403)
	if err := tx.Exec("UPDATE admin_user SET auth_version=auth_version+1 WHERE id=?", id).Error; err != nil {
		t.Fatal(err)
	}
	call("GET", base+"/resources", nil, 401)
}
