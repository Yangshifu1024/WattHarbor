//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"wattharbor/internal/domain/operations"
	adminaccounthttp "wattharbor/internal/http/admin/account"
	admindashboard "wattharbor/internal/http/admin/dashboard"
	adminops "wattharbor/internal/http/admin/ops"
	authhttp "wattharbor/internal/http/auth"
	"wattharbor/internal/ops"
	"wattharbor/internal/platform/auth"
	"wattharbor/internal/platform/httpapi"
	"wattharbor/internal/testfixture"
)

// TestRegisteredAdminManagement 使用实际账号／角色注册路由和真实 SQL／Redis 验证输入、状态、撤权及安全审计。
// SQL 造数在外层回滚，随机 SID 单独注销；保留 bootstrap 及内置权限，不更改开发账号。
func TestRegisteredAdminManagement(t *testing.T) {
	tx := cardRouteDB(t, "TEST_MYSQL_URL").Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	cache := identityRouteRedis(t)
	ctx := context.Background()
	id := testfixture.NewID()
	tag := strings.ReplaceAll(uuid.NewString(), "-", "_")
	for _, seed := range []struct {
		table string
		row   map[string]any
	}{
		{"role", map[string]any{"id": id + 1, "code": "registered_" + tag, "name": "注册管理操作者", "is_builtin": false}},
		{"admin_user", map[string]any{"id": id, "username": "management_" + tag, "password_hash": "private_hash_fixture", "role_id": id + 1, "status": "active", "auth_version": 9007199254741111, "mfa_secret": "private_mfa_fixture"}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(err)
		}
	}
	permissions := []string{"admin_user.read", "admin_user.create", "admin_user.update", "admin_user.delete", "admin_user.reset_password", "role.create", "role.update", "role.delete", "order.read", "announcement.create", "announcement.read", "audit.read", "dashboard.read"}
	for _, code := range permissions {
		if err := tx.Exec("INSERT INTO role_permission(role_id,permission_id) SELECT ?,id FROM permission WHERE code=?", id+1, code).Error; err != nil {
			t.Fatal(err)
		}
	}
	repo := ops.SessionRepo{Redis: cache}
	jwt, err := auth.NewJWT(strings.Repeat("registered-management-", 3))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := (ops.AccountRepo{DB: tx}).Profile(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	sid, _, err := repo.Create(ctx, operations.AccountIdentity{ID: id, Username: profile.Username, AuthVersion: profile.AuthVersion})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.RevokeSession(ctx, sid)
	token, err := (ops.JWTAccess{JWT: jwt}).Sign(profile, sid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	authAPI := authhttp.AdminAuth{Sessions: ops.SessionService{Accounts: ops.AccountRepo{DB: tx}, Sessions: repo, Tokens: ops.JWTAccess{JWT: jwt}}}
	accounts := ops.AccountService{DB: tx}
	roles := ops.RoleService{DB: tx}
	router := httpapi.NewRouter()
	api := adminaccounthttp.AdminAccounts{Accounts: accounts, Roles: roles, Auth: &authAPI}
	api.Register(router)
	api.RegisterRoles(router)
	(adminops.API{Accounts: accounts, Announcements: ops.AnnouncementService{DB: tx}, Audits: ops.AuditQueryService{DB: tx}, Auth: authAPI}).Register(router)
	(admindashboard.Dashboard{Queries: ops.DashboardQueryService{DB: tx}}).Register(router, authAPI)
	call := func(method, path string, input any, status int) json.RawMessage {
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
		if w.Code != status {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, w.Code, status, w.Body)
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(w.Body.String(), "private_hash_fixture") || strings.Contains(w.Body.String(), "private_mfa_fixture") || strings.Contains(w.Body.String(), "password_hash") || strings.Contains(w.Body.String(), "mfa_secret") {
			t.Fatal("credential leaked in management response")
		}
		return envelope.Data
	}
	readID := func(raw json.RawMessage) uint64 {
		t.Helper()
		var reply struct {
			ID uint64 `json:"id"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil || reply.ID == 0 {
			t.Fatalf("id reply %s %v", raw, err)
		}
		return reply.ID
	}
	base := "/api/v1/admin/"
	// 注册路由保留公告空值、审计筛选及看板故障 503；业务造数由外层事务回滚。
	announcementInput := map[string]any{"title": "registered_" + tag, "content": "中文公告", "scope": "global", "start_at": time.Now().UTC()}
	badAnnouncement := map[string]any{"title": "无效", "content": "内容", "scope": "global", "start_at": time.Now().UTC(), "unknown": true}
	call("POST", base+"announcements", badAnnouncement, 400)
	announcement := readID(call("POST", base+"announcements", announcementInput, 200))
	var announcements struct {
		Items []ops.AnnouncementRow `json:"items"`
	}
	if err := json.Unmarshal(call("GET", base+"announcements", nil, 200), &announcements); err != nil {
		t.Fatal(err)
	}
	foundAnnouncement := false
	for _, row := range announcements.Items {
		if row.ID == announcement {
			foundAnnouncement = true
			if string(row.TargetIDs) != "null" || row.EndAt != nil || row.Status != "published" {
				t.Fatalf("original announcement %+v", row)
			}
		}
	}
	if !foundAnnouncement {
		t.Fatal("registered announcement absent")
	}
	call("GET", base+"audit-logs?from=invalid", nil, 400)
	var audits struct {
		Total int64          `json:"total"`
		Items []ops.AuditRow `json:"items"`
	}
	if err := json.Unmarshal(call("GET", base+"audit-logs?module=announcement&actor="+profile.Username, nil, 200), &audits); err != nil || audits.Total != 1 || len(audits.Items) != 1 || audits.Items[0].BeforeJSON == nil || *audits.Items[0].BeforeJSON != "null" {
		t.Fatalf("registered audits %+v %v", audits, err)
	}
	var metrics ops.DashboardMetrics
	if err := json.Unmarshal(call("GET", base+"dashboard", nil, 200), &metrics); err != nil || len(metrics.DailyTrend) != 7 {
		t.Fatalf("registered metrics %+v %v", metrics, err)
	}
	dashboardFailure := errors.New("registered private dashboard failure")
	dashboardCallback := "registered-dashboard-" + tag
	if err := tx.Callback().Query().Before("gorm:query").Register(dashboardCallback, func(q *gorm.DB) {
		if q.Statement.Table == "charge_order" {
			q.AddError(dashboardFailure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if raw := call("GET", base+"dashboard", nil, 503); string(raw) != "null" {
		t.Fatalf("partial dashboard %s", raw)
	}
	tx.Callback().Query().Remove(dashboardCallback)
	// 鉴权首次读取后才撤权／提升版本，使服务第二次读取真正遇到权限变化。
	for _, versionChange := range []bool{false, true} {
		changed := false
		key := "registered-dashboard-admission-" + tag
		if err := tx.Callback().Query().After("gorm:query").Register(key, func(q *gorm.DB) {
			if changed || !strings.Contains(q.Statement.SQL.String(), "JOIN role_permission AS rp") {
				return
			}
			changed = true
			var err error
			if versionChange {
				err = tx.Exec("UPDATE admin_user SET auth_version=auth_version+1 WHERE id=?", id).Error
			} else {
				err = tx.Exec("DELETE rp FROM role_permission rp JOIN permission p ON p.id=rp.permission_id WHERE rp.role_id=? AND p.code='dashboard.read'", id+1).Error
			}
			if err != nil {
				q.AddError(err)
			}
		}); err != nil {
			t.Fatal(err)
		}
		status := 403
		if versionChange {
			status = 401
		}
		call("GET", base+"dashboard", nil, status)
		tx.Callback().Query().Remove(key)
		if !changed {
			t.Fatal("admission fixture not reached")
		}
		if versionChange {
			if err := tx.Exec("UPDATE admin_user SET auth_version=? WHERE id=?", profile.AuthVersion, id).Error; err != nil {
				t.Fatal(err)
			}
		} else if err := tx.Exec("INSERT INTO role_permission(role_id,permission_id) SELECT ?,id FROM permission WHERE code='dashboard.read'", id+1).Error; err != nil {
			t.Fatal(err)
		}
	}
	call("GET", base+"admin-users?page_size=0", nil, 400)
	call("GET", base+"admin-users", nil, 200)
	call("GET", base+"roles", nil, 200)
	call("GET", base+"permissions", nil, 200)
	call("POST", base+"roles", map[string]any{"code": "invalid", "name": "越权", "permissions": []string{"wallet.read"}}, 400)
	roleInput := map[string]any{"code": "target_" + tag, "name": "角色注册", "permissions": []string{"order.read"}}
	role := readID(call("POST", base+"roles", roleInput, 200))
	call("POST", base+"roles", roleInput, 409)
	password := "Registered!Management2026"
	userInput := map[string]any{"username": "target_" + tag, "password": password, "display_name": "注册账号", "role_id": role}
	bad := map[string]any{"username": "bad_" + tag, "password": password, "role_id": role, "unknown": true}
	call("POST", base+"admin-users", bad, 400)
	user := readID(call("POST", base+"admin-users", userInput, 200))
	userPath := base + fmt.Sprint("admin-users/", user)
	call("POST", base+"admin-users", userInput, 409)
	call("DELETE", base+fmt.Sprint("roles/", role), nil, 409)
	call("PUT", userPath, map[string]any{"status": "unknown"}, 400)
	raw := call("PUT", userPath, map[string]any{"display_name": "新名称", "role_id": role}, 200)
	if !strings.Contains(string(raw), `"sessions_revoked":false`) {
		t.Fatalf("same role revoked %s", raw)
	}
	raw = call("PUT", userPath, map[string]any{"status": "disabled"}, 200)
	if !strings.Contains(string(raw), `"sessions_revoked":true`) {
		t.Fatalf("disable not revoked %s", raw)
	}
	call("POST", userPath+"/unlock", nil, 200)
	call("POST", userPath+"/reset-password", map[string]any{"new_password": "Reset!Registered2026"}, 200)
	call("POST", base+fmt.Sprint("admin-users/", id, "/mfa"), map[string]any{"action": "disable"}, 409)
	var enrolled struct {
		// Secret 本测试启用 MFA 的验证密钥，不是生产凭据。
		Secret string `json:"secret"`
		// URI 本测试核对的 MFA 配置 URI。
		URI  string `json:"otpauth_uri"`
		Code string `json:"code"`
		// Enabled 本断言核对的 MFA 启用状态。
		Enabled bool `json:"enabled"`
	}
	raw = call("POST", userPath+"/mfa", map[string]any{"action": "enrol"}, 200)
	if err := json.Unmarshal(raw, &enrolled); err != nil || enrolled.Secret == "" || enrolled.Code == "" || enrolled.Enabled {
		t.Fatalf("enrol contract %v", err)
	}
	code, err := auth.TOTPCode(enrolled.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call("POST", userPath+"/mfa", map[string]any{"action": "confirm", "secret": enrolled.Secret, "code": code}, 200)
	call("POST", userPath+"/mfa", map[string]any{"action": "disable"}, 200)
	call("DELETE", base+fmt.Sprint("admin-users/", id), nil, 409)
	call("DELETE", userPath, nil, 200)
	call("DELETE", userPath, nil, 404)
	call("DELETE", base+fmt.Sprint("roles/", role), nil, 200)
	call("DELETE", base+fmt.Sprint("roles/", role), nil, 404)
	// 实际注册处理器的审计失败必须回滚整个创建，返回公开 503 而非暴露存储细节。
	failure := errors.New("registered management private audit failure")
	callback := "registered-management-audit"
	if err := tx.Callback().Create().Before("gorm:create").Register(callback, func(q *gorm.DB) {
		if q.Statement.Table == "audit_log" {
			q.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	roleInput["code"] = "failed_" + tag
	call("POST", base+"roles", roleInput, 503)
	tx.Callback().Create().Remove(callback)
	var count int64
	if err := tx.Table("role").Where("code=?", roleInput["code"]).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("audit failure committed role %d %v", count, err)
	}
	var snapshots []struct {
		// Before 审计中的变更前载荷。
		Before string `gorm:"column:before_json"`
		// After 审计中的变更后载荷。
		After string `gorm:"column:after_json"`
	}
	if err := tx.Table("audit_log").Where("actor_id=?", id).Find(&snapshots).Error; err != nil {
		t.Fatal(err)
	}
	if len(snapshots) < 10 {
		t.Fatalf("audits=%d", len(snapshots))
	}
	for _, row := range snapshots {
		for _, secret := range []string{"password_hash", "mfa_secret", password, "private_hash_fixture", "private_mfa_fixture", enrolled.Secret, code} {
			if strings.Contains(row.Before+row.After, secret) {
				t.Fatal("credential leaked in registered audit")
			}
		}
	}
	// 删除实际权限关联后，同一 JWT 在下一受保护请求立即拒绝。
	if err := tx.Exec("DELETE rp FROM role_permission rp JOIN permission p ON p.id=rp.permission_id WHERE rp.role_id=? AND p.code='admin_user.read'", id+1).Error; err != nil {
		t.Fatal(err)
	}
	call("GET", base+"roles", nil, 403)
}
