//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"chargepilot/internal/domain/operations"
	adminaccounthttp "chargepilot/internal/http/admin/account"
	authhttp "chargepilot/internal/http/auth"
	"chargepilot/internal/ops"
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/testfixture"
	"github.com/google/uuid"
)

// TestRegisteredAdminAccountKeywordUsesLiteralCharacters 用实际注册 GET、真实 MySQL 与 Redis 会话验证账号关键词的文字匹配。
// 私有角色、账号及权限映射由同一外层事务回滚，不修改已有角色；只读请求不生成审计或 Outbox。
// ASCII 目标及相邻干扰项区分 _、%、! 和普通关键词，并核对原状态筛选、软删除排除及分页顺序。
func TestRegisteredAdminAccountKeywordUsesLiteralCharacters(t *testing.T) {
	db := cardRouteDB(t, "TEST_MYSQL_URL")
	cache := identityRouteRedis(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	id := testfixture.NewID()
	roleID := id + 1
	ids := []uint64{id}
	for offset := uint64(10); offset <= 19; offset++ {
		ids = append(ids, id+offset)
	}
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(err)
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		for _, check := range []struct {
			table  string   // 本测试实际写入的表，不涉及审计或事件队列。
			column string   // 精确主键或私有角色外键，用于确认回滚而非跨身份删除。
			values []uint64 // 本测试拥有的完整身份集合。
		}{{"admin_user", "id", ids}, {"role", "id", []uint64{roleID}}, {"role_permission", "role_id", []uint64{roleID}}} {
			var remaining int64
			if err := db.WithContext(cleanupCtx).Table(check.table).Where(check.column+" IN ?", check.values).Count(&remaining).Error; err != nil || remaining != 0 {
				t.Errorf("keyword fixture rollback %s remaining=%d err=%v", check.table, remaining, err)
			}
		}
	})
	tag := strings.ReplaceAll(uuid.NewString(), "-", "")
	prefix := "kw" + tag
	actorUsername := "actor" + tag
	if err := tx.Table("role").Create(map[string]any{"id": roleID, "code": "keyword" + tag, "name": "keyword fixture", "is_builtin": false}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("admin_user").Create(map[string]any{"id": id, "username": actorUsername, "password_hash": "keyword_private_hash", "role_id": roleID, "status": "active", "auth_version": uint64(7)}).Error; err != nil {
		t.Fatal(err)
	}
	permission := tx.Exec("INSERT INTO role_permission(role_id,permission_id) SELECT ?,id FROM permission WHERE code=?", roleID, "admin_user.read")
	if permission.Error != nil || permission.RowsAffected != 1 {
		t.Fatalf("private read permission rows=%d err=%v", permission.RowsAffected, permission.Error)
	}
	// keywordAccount 只映射私有列表夹具；显示名允许符号，所有登录名保持合法字符与长度。
	type keywordAccount struct {
		offset   uint64 // 相对随机主键基址的唯一偏移。
		username string // 本测试创建的合法登录名，最长不超过 64 字节。
		display  string // ASCII 显示名，含目标符号或相邻干扰字符。
		status   string // active 或 disabled 状态，供真实筛选。
		deleted  bool   // 是否软删除；删除后不能出现在列表。
	}
	rows := []keywordAccount{
		{10, prefix + "u_needle", "plain username target", "active", false},
		{11, prefix + "uxneedle", "plain username neighbor", "active", false},
		{12, prefix + "row12", prefix + "d_needle", "active", false},
		{13, prefix + "row13", prefix + "dxneedle", "active", false},
		{14, prefix + "row14", prefix + "d_needle", "disabled", false},
		{15, prefix + "row15", prefix + "d_needle", "active", true},
		{16, prefix + "row16", prefix + "d!needle", "active", false},
		{17, prefix + "row17", prefix + "d!!needle", "active", false},
		{18, prefix + "row18", prefix + "d%needle", "active", false},
		{19, prefix + "row19", prefix + "d!xneedle", "active", false},
	}
	for _, row := range rows {
		values := map[string]any{"id": id + row.offset, "username": row.username, "display_name": row.display, "password_hash": "keyword_private_hash", "role_id": roleID, "status": row.status}
		if row.deleted {
			values["deleted_at"] = time.Now().UTC()
		}
		if err := tx.Table("admin_user").Create(values).Error; err != nil {
			t.Fatal(err)
		}
	}
	profile, err := (ops.AccountRepo{DB: tx}).Profile(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	sessions := ops.SessionRepo{Redis: cache}
	sid, _, err := sessions.Create(ctx, operations.AccountIdentity{ID: id, Username: actorUsername, AuthVersion: profile.AuthVersion})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := sessions.RevokeSession(cleanupCtx, sid); err != nil {
			t.Error(err)
		}
		if exists, err := sessions.Exists(cleanupCtx, sid); err != nil || exists {
			t.Errorf("private keyword SID remains=%t err=%v", exists, err)
		}
	})
	jwt, err := auth.NewJWT(strings.Repeat("keyword-route-test-", 3))
	if err != nil {
		t.Fatal(err)
	}
	token, err := (ops.JWTAccess{JWT: jwt}).Sign(profile, sid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	authAPI := authhttp.AdminAuth{Sessions: ops.SessionService{Accounts: ops.AccountRepo{DB: tx}, Sessions: sessions, Tokens: ops.JWTAccess{JWT: jwt}}}
	router := httpapi.NewRouter()
	(adminaccounthttp.AdminAccounts{Accounts: ops.AccountService{DB: tx}, Auth: &authAPI}).Register(router)
	base := "/api/v1/admin/admin-users"
	anonymous := httptest.NewRecorder()
	router.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, base, nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list status=%d", anonymous.Code)
	}
	for _, test := range []struct {
		name       string   // 独立命名的输入与筛选场景。
		keyword    string   // 经 URL 编码交给真实分页解析器的文字关键词。
		status     string   // 原状态筛选，空值不加状态条件。
		page, size int      // 分页请求，不改变排序或计数规则。
		total      int64    // 匹配未删除夹具的预期总数，独立于页长。
		offsets    []uint64 // 当页按主键倒序返回的私有身份。
	}{
		{"username underscore", prefix + "u_needle", "", 1, 20, 1, []uint64{10}},
		{"display underscore", prefix + "d_needle", "", 1, 20, 2, []uint64{14, 12}},
		{"display percent", prefix + "d%needle", "", 1, 20, 1, []uint64{18}},
		{"display escape character", prefix + "d!needle", "", 1, 20, 1, []uint64{16}},
		{"display repeated escape", prefix + "d!!needle", "", 1, 20, 1, []uint64{17}},
		{"literal active", prefix + "d_needle", "active", 1, 20, 1, []uint64{12}},
		{"literal disabled", prefix + "d_needle", "disabled", 1, 20, 1, []uint64{14}},
		{"literal second page", prefix + "d_needle", "", 2, 1, 2, []uint64{12}},
		{"ordinary keyword", prefix, "", 1, 20, 9, []uint64{19, 18, 17, 16, 14, 13, 12, 11, 10}},
		{"ordinary keyword active second page", prefix, "active", 2, 2, 8, []uint64{17, 16}},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := url.Values{"keyword": {test.keyword}, "page": {fmt.Sprint(test.page)}, "page_size": {fmt.Sprint(test.size)}, "status": {test.status}}
			request := httptest.NewRequest(http.MethodGet, base+"?"+query.Encode(), nil).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer "+token)
			reply := httptest.NewRecorder()
			router.ServeHTTP(reply, request)
			var envelope struct {
				Code int `json:"code"` // HTTP 信封业务码，成功须为 0。
				Data struct {
					Items    []ops.AdminUserRow `json:"items"`     // 实际注册路由返回的账号安全信息。
					Total    int64              `json:"total"`     // 筛选总数，不因分页截断。
					Page     int                `json:"page"`      // 响应页码。
					PageSize int                `json:"page_size"` // 响应页长。
				} `json:"data"` // 列表响应载荷，不替换公开契约。
			}
			if err := json.Unmarshal(reply.Body.Bytes(), &envelope); err != nil || reply.Code != http.StatusOK || envelope.Code != 0 {
				t.Fatalf("keyword route HTTP=%d body=%s err=%v", reply.Code, reply.Body, err)
			}
			page := envelope.Data
			if page.Total != test.total || page.Page != test.page || page.PageSize != test.size || len(page.Items) != len(test.offsets) {
				t.Fatalf("keyword %q page=%+v want total=%d page=%d size=%d offsets=%v", test.keyword, page, test.total, test.page, test.size, test.offsets)
			}
			for i, offset := range test.offsets {
				if page.Items[i].ID != id+offset || test.status != "" && page.Items[i].Status != test.status {
					t.Fatalf("keyword %q row=%+v want id=%d status=%q", test.keyword, page.Items[i], id+offset, test.status)
				}
			}
			if strings.Contains(reply.Body.String(), "keyword_private_hash") || strings.Contains(reply.Body.String(), "password_hash") || strings.Contains(reply.Body.String(), "mfa_secret") {
				t.Fatal("list leaked private account credentials")
			}
		})
	}
}
