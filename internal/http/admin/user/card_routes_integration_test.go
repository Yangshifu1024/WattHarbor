//go:build integration

package user

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/card"
	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// cardAdminSessionHook 仅提供会话存在及原账号版本 fixture；权限、用户、卡与审计均为真实库。
type cardAdminSessionHook struct {
	records map[string]string // admin:session:<sid> 对应固定用户与权限版本 JSON
}

// DialHook 拒绝 next 对应的外部 Redis 连接；测试只允许 ProcessHook 提供的会话读。
func (cardAdminSessionHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, errors.New("session fixture cannot dial")
	}
}

// ProcessHook 用 ctx 下 cmd 的键返回固定 EXISTS／GET；未声明命令报错，不调用 next 或真实 Redis。
func (h cardAdminSessionHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		key, ok := cmd.Args()[1].(string)
		if !ok {
			return errors.New("invalid fixture key")
		}
		record, exists := h.records[key]
		switch v := cmd.(type) {
		case *redis.IntCmd:
			if v.Name() != "exists" {
				return errors.New("unexpected int command")
			}
			if exists {
				v.SetVal(1)
			} else {
				v.SetVal(0)
			}
			return nil
		case *redis.StringCmd:
			if v.Name() != "get" {
				return errors.New("unexpected string command")
			}
			if !exists {
				v.SetErr(redis.Nil)
				return redis.Nil
			}
			v.SetVal(record)
			return nil
		default:
			return errors.New("unexpected session command")
		}
	}
}

// ProcessPipelineHook 拒绝 ctx 下 cmds 批量读写；fixture 不声明流水线能力，不调用 next。
func (cardAdminSessionHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error { return errors.New("unexpected session pipeline") }
}

// cardAdminDB 打开实际 TEST_MYSQL_URL 交由 t 关闭；缺配置或失败终止，不跳过。
func cardAdminDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_URL")
	if raw == "" {
		t.Fatal("TEST_MYSQL_URL is required")
	}
	sql, err := dbconn.Open(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sql.Close() })
	orm, err := dbconn.WrapGORM(sql)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}

// TestRegisteredCardManagementPreservesPermissionInputAndAudit 覆盖实际后台注册、实时角色权限及原卡审计事务。
// 整个造数外层回滚；会话存在为 fixture，真实 Redis 生命周期由 OP-01 验收，不修改 bootstrap 或既有角色。
func TestRegisteredCardManagementPreservesPermissionInputAndAudit(t *testing.T) {
	db := cardAdminDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := uint64(time.Now().UnixNano())
	sid, readerSid := uuid.NewString(), uuid.NewString()
	var role, readPermission uint64
	if err := tx.Table("role").Where("code='customer_admin' AND deleted_at IS NULL").Pluck("id", &role).Error; err != nil || role == 0 {
		t.Fatalf("role=%d %v", role, err)
	}
	if err := tx.Table("permission").Where("code='user.read'").Pluck("id", &readPermission).Error; err != nil || readPermission == 0 {
		t.Fatalf("permission=%d %v", readPermission, err)
	}
	for _, seed := range []struct {
		// table 是隔离造数的所属表。
		table string
		// row 包含本场景的唯一身份与原权限材料，外层事务结束后回滚。
		row map[string]any
	}{{"role", map[string]any{"id": id + 2, "code": "card-read-" + sid, "name": "test card reader", "is_builtin": false}}, {"role_permission", map[string]any{"role_id": id + 2, "permission_id": readPermission}}, {"admin_user", map[string]any{"id": id + 10, "username": "card-admin-" + sid, "password_hash": "fixture", "role_id": role, "status": "active", "auth_version": 1}}, {"admin_user", map[string]any{"id": id + 11, "username": "card-reader-" + sid, "password_hash": "fixture", "role_id": id + 2, "status": "active", "auth_version": 1}}, {"user", map[string]any{"id": id, "openid": sid, "status": "active"}}, {"user", map[string]any{"id": id + 1, "openid": readerSid, "status": "active"}}} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatalf("seed %s: %v", seed.table, err)
		}
	}
	jwt, err := auth.NewJWT("registered-card-admin-secret-at-least-32")
	if err != nil {
		t.Fatal(err)
	}
	sign := func(uid uint64, sid string) string {
		token, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(uid, 10), Kind: "admin", SessionID: sid, IssuedAt: time.Now().Add(-time.Minute).Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	token, reader := sign(id+10, sid), sign(id+11, readerSid)
	record, _ := json.Marshal(map[string]any{"uid": id + 10, "version": 1})
	readerRecord, _ := json.Marshal(map[string]any{"uid": id + 11, "version": 1})
	cache := redis.NewClient(&redis.Options{Addr: "fixture-only"})
	defer cache.Close()
	cache.AddHook(cardAdminSessionHook{records: map[string]string{"admin:session:" + sid: string(record), "admin:session:" + readerSid: string(readerRecord)}})
	router := httpapi.NewRouter()
	API{Cards: card.CardAdminService{DB: tx}, Auth: authhttp.AdminAuth{Sessions: ops.SessionService{Accounts: ops.AccountRepo{DB: tx}, Sessions: ops.SessionRepo{Redis: cache}, Tokens: ops.JWTAccess{JWT: jwt}}}}.Register(router)
	call := func(method, path, body, bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	const bind = "/api/v1/admin/online-cards/bind"
	number := strconv.FormatUint(id%4000000000+1, 10)
	body := fmt.Sprintf("{\"card_no\":\"%s\",\"user_id\":\"%d\",\"reason\":\"verified owner\"}", number, id)
	if w := call("GET", "/api/v1/admin/online-cards", "", ""); w.Code != 401 {
		t.Fatalf("anonymous=%d", w.Code)
	}
	if w := call("POST", bind, body, reader); w.Code != 403 {
		t.Fatalf("reader mutation=%d %s", w.Code, w.Body)
	}
	if w := call("GET", "/api/v1/admin/online-cards", "", reader); w.Code != 200 {
		t.Fatalf("reader list=%d %s", w.Code, w.Body)
	}
	for _, invalid := range []string{strings.TrimSuffix(body, "}") + ",\"unknown\":true}", fmt.Sprintf("{\"card_no\":\"%s\",\"user_id\":%d,\"reason\":\"verified owner\"}", number, id), strings.Replace(body, number, "12345678901234567890", 1)} {
		if w := call("POST", bind, invalid, token); w.Code != 400 {
			t.Fatalf("invalid input=%d %s", w.Code, w.Body)
		}
	}
	w := call("POST", bind, body, token)
	if w.Code != 200 {
		t.Fatalf("bind=%d %s", w.Code, w.Body)
	}
	var row card.OnlineCard
	if err := tx.Where("card_no=?", number).Take(&row).Error; err != nil || row.UserID != id {
		t.Fatalf("binding=%+v %v", row, err)
	}
	if w := call("POST", bind, body, token); w.Code != 200 {
		t.Fatalf("replay=%d %s", w.Code, w.Body)
	}
	if w := call("POST", bind, strings.Replace(body, strconv.FormatUint(id, 10), strconv.FormatUint(id+1, 10), 1), token); w.Code != 409 {
		t.Fatalf("owner conflict=%d %s", w.Code, w.Body)
	}
	path := "/api/v1/admin/online-cards/" + strconv.FormatUint(row.ID, 10) + "/status"
	fault := errors.New("registered card audit failure")
	const callback = "registered_card_audit_fault"
	if err := tx.Callback().Create().Before("gorm:create").Register(callback, func(db *gorm.DB) {
		if db.Statement.Table == "online_card_audit" {
			db.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	w = call("POST", path, "{\"status\":\"lost\",\"reason\":\"verified missing\"}", token)
	tx.Callback().Create().Remove(callback)
	if w.Code != 503 {
		t.Fatalf("audit failure=%d %s", w.Code, w.Body)
	}
	if err := tx.Where("id=?", row.ID).Take(&row).Error; err != nil || row.Status != "active" {
		t.Fatalf("partial card status=%+v %v", row, err)
	}
	if w := call("POST", path, "{\"status\":\"lost\",\"reason\":\"verified missing\"}", token); w.Code != 200 {
		t.Fatalf("lost=%d %s", w.Code, w.Body)
	}
	var audits int64
	if err := tx.Table("online_card_audit").Where("card_id=?", row.ID).Count(&audits).Error; err != nil || audits != 2 {
		t.Fatalf("audits=%d %v", audits, err)
	}
	if err := tx.Table("admin_user").Where("id=?", id+10).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if w := call("POST", path, "{\"status\":\"active\",\"reason\":\"revoked actor\"}", token); w.Code != 401 {
		t.Fatalf("actor revoked=%d %s", w.Code, w.Body)
	}
}
