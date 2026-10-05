//go:build integration

package finance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	chargehttp "github.com/ChargePilot2026/charge-pilot/internal/interface/http"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// routeSessionHook 仅提供测试会话记录；权限、账号及发票审核使用真实 MySQL。
// 此 fixture 不验证 Redis 生命周期或 Lua，会话真实实现的迁移验收属于 OP-01。
type routeSessionHook struct {
	records map[string]string // admin:session:<sid> 对应固定用户与权限版本 JSON
}

// DialHook 拒绝 next 对应的外部 Redis 连接；测试只允许 ProcessHook 提供的会话读。
func (routeSessionHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, errors.New("session fixture cannot dial")
	}
}

// ProcessHook 用 ctx 下 cmd 的键返回固定 EXISTS／GET；未声明命令报错，不调用 next 或真实 Redis。
func (h routeSessionHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
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
func (routeSessionHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error { return errors.New("unexpected session pipeline") }
}

// TestRegisteredInvoiceReviewKeepsRealAuthorizationAndAtomicAudit 验证完整注册路由、当前角色、严格输入和首签撤权。
// 会话存在性为 fixture，账号／角色／权限／审核／审计均为真实 MySQL；外层事务回滚全部造数。
func TestRegisteredInvoiceReviewKeepsRealAuthorizationAndAtomicAudit(t *testing.T) {
	url := os.Getenv("TEST_MYSQL_URL")
	if url == "" {
		t.Fatal("TEST_MYSQL_URL 未设置，不能省略真库验收")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	tx := orm.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := uint64(time.Now().UnixNano())
	var roleID uint64
	if err := tx.Table("role").Where("code='customer_finance' AND deleted_at IS NULL").Pluck("id", &roleID).Error; err != nil || roleID == 0 {
		t.Fatalf("finance role=%d err=%v", roleID, err)
	}
	jwt, err := auth.NewJWT("finance-route-secret-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	sessions := redis.NewClient(&redis.Options{Addr: "fixture-only"})
	defer sessions.Close()
	records := map[string]string{}
	tokens := []string{}
	for i := uint64(0); i < 2; i++ {
		actorID := id + i
		sid := uuid.NewString()
		if err := tx.Table("admin_user").Create(map[string]any{"id": actorID, "username": "invoice-route-" + sid, "password_hash": "fixture", "role_id": roleID, "status": "active", "auth_version": 1}).Error; err != nil {
			t.Fatal(err)
		}
		record, _ := json.Marshal(map[string]any{"uid": actorID, "version": 1})
		records["admin:session:"+sid] = string(record)
		now := time.Now()
		token, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(actorID, 10), Kind: "admin", SessionID: sid, IssuedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
	sessions.AddHook(routeSessionHook{records: records})
	store := ops.AccountRepo{DB: tx}
	service := settlement.InvoiceReviewService{DB: tx, ReviewRepo: settlement.InvoiceReviewRepo{LookupReviewer: func(ctx context.Context, reviewerID uint64) (settlement.ReviewerStatus, error) {
		p, err := store.Profile(ctx, reviewerID)
		return settlement.ReviewerStatus{Role: p.Role, Permitted: p.HasPermission("invoice.review")}, err
	}}}
	if err := tx.Table("invoice_request").Create(map[string]any{"id": id, "invoice_no": "INV-" + uuid.NewString(), "user_id": id, "biz_type": "charge", "biz_id": id, "total_cents": 400, "invoice_type": "normal", "title": "真实审核路由", "review_status": "pending"}).Error; err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter()
	API{DB: tx, Auth: chargehttp.AdminAuth{Sessions: ops.SessionService{Accounts: store, Sessions: ops.SessionRepo{Redis: sessions}, Tokens: ops.JWTAccess{JWT: jwt}}}, Invoices: service}.Register(router)
	path := "/api/v1/admin/billing/invoices/" + strconv.FormatUint(id, 10) + "/approve"
	post := func(body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	body := `{"invoice_url":"https://example.test/invoice.pdf"}`
	if w := post(body, ""); w.Code != 401 {
		t.Fatalf("anonymous=%d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{`{"invoice_url":"http://example.test/invoice.pdf"}`, `{"invoice_url":"https://example.test/invoice.pdf","total_cents":1}`, body + " {}"} {
		if w := post(bad, tokens[0]); w.Code != 400 {
			t.Fatalf("invalid=%d %s", w.Code, w.Body.String())
		}
	}
	if w := post(body, tokens[0]); w.Code != 200 || !strings.Contains(w.Body.String(), `"review_status":"awaiting_second"`) {
		t.Fatalf("first=%d %s", w.Code, w.Body.String())
	}
	if w := post(body, tokens[0]); w.Code != 409 {
		t.Fatalf("same signer=%d %s", w.Code, w.Body.String())
	}
	if err := tx.Table("admin_user").Where("id=?", id).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if w := post(body, tokens[1]); w.Code != 409 {
		t.Fatalf("revoked first signer=%d %s", w.Code, w.Body.String())
	}
	if err := tx.Table("admin_user").Where("id=?", id).Update("status", "active").Error; err != nil {
		t.Fatal(err)
	}
	if w := post(body, tokens[1]); w.Code != 200 || !strings.Contains(w.Body.String(), `"review_status":"approved"`) {
		t.Fatalf("second=%d %s", w.Code, w.Body.String())
	}
	var status string
	if err := tx.Table("invoice_request").Where("id=?", id).Pluck("review_status", &status).Error; err != nil || status != "issued" {
		t.Fatalf("invoice=%q err=%v", status, err)
	}
	var count int64
	if err := tx.Table("audit_log").Where("target_type='invoice' AND target_id=?", strconv.FormatUint(id, 10)).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("audit=%d err=%v", count, err)
	}
}
