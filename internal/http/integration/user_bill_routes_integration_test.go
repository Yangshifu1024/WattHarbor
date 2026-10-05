//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authhttp "github.com/ChargePilot2026/charge-pilot/internal/http/auth"
	usersettlement "github.com/ChargePilot2026/charge-pilot/internal/http/user/settlement"
	"github.com/ChargePilot2026/charge-pilot/internal/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/settlement"
	"gorm.io/gorm"
)

// TestRegisteredUserBillServicePreservesOwnershipAndRead 验证所属服务后的三个真实注册路由。
// 账单、已读与资金投影来自实际MySQL，造数外层回滚；身份为窄fixture，指定回调故障只验证原错误阶段映射。
func TestRegisteredUserBillServicePreservesOwnershipAndRead(t *testing.T) {
	db := cardRouteDB(t, "TEST_MYSQL_URL")
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := uint64(time.Now().UnixNano())
	now := time.Now().UTC().Truncate(time.Millisecond)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i, userID := range []uint64{17, 18, 17} {
		row := map[string]any{
			"id": id + uint64(i), "bill_no": fmt.Sprintf("B%020d", id+uint64(i)),
			"charge_order_id": id + uint64(i), "user_id": userID, "device_id": "bill-fixture", "port_no": 1,
			"electric_cents": 0, "service_cents": 7, "total_cents": 7, "prepaid_cents": 100,
			"refund_cents": 93, "shortfall_cents": 0, "status": "issued", "issued_at": now, "created_month": month,
		}
		if err := tx.Table("charge_bill").Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	fixture := deniedUserIdentity{}
	a := authhttp.SessionAuthenticator{Auth: identity.Authenticator{Tokens: fixture, Sessions: fixture, Users: fixture}}
	service := settlement.UserBillService{Repo: settlement.BillRepo{DB: tx}}
	r := httpapi.NewRouter()
	usersettlement.BillAPI{Auth: a, Bills: service}.Register(r)
	// call 在实际注册端点发送原身份，核对status及单信封；返回UseNumber保留精度的公开data。
	call := func(method, path string, status int) any {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer opaque")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private-bill-failure") {
			t.Fatal("storage cause leaked")
		}
		d := json.NewDecoder(w.Body)
		d.UseNumber()
		var reply httpapi.Envelope
		if err := d.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		if err := d.Decode(new(any)); err != io.EOF {
			t.Fatalf("not a single reply: %v", err)
		}
		if status == 200 && reply.Code != 0 || status == 404 && reply.Code != 1004 || status == 503 && reply.Code != 5003 {
			t.Fatalf("invalid bill reply: %+v", reply)
		}
		return reply.Data
	}
	path := fmt.Sprintf("/api/v1/user/bills/%d", id)
	detail := call("GET", path, 200).(map[string]any)
	if detail["user_id"] != "17" || detail["total_cents"] != json.Number("7") || detail["refund_cents"] != json.Number("93") || detail["payment_order_id"] != nil || detail["charged_kwh"] != nil {
		t.Fatalf("original bill projection changed: %v", detail)
	}
	other := fmt.Sprintf("/api/v1/user/bills/%d", id+1)
	call("GET", other, 404)
	call("POST", other+"/read", 404)
	for range 2 {
		call("POST", path+"/read", 200)
	}
	var count int64
	if err := tx.Table("charge_bill_read").Where("bill_id IN ?", []uint64{id, id + 1}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("owner/replay read rows=%d: %v", count, err)
	}
	page := call("GET", "/api/v1/user/bills", 200).(map[string]any)
	if page["total"] != json.Number("2") {
		t.Fatalf("owner-scoped total: %v", page)
	}
	seenRead := false
	for _, value := range page["items"].([]any) {
		row := value.(map[string]any)
		if row["user_id"] != "17" {
			t.Fatalf("other user bill leaked: %v", row)
		}
		if row["id"] == json.Number(fmt.Sprint(id)) {
			seenRead = row["read"] == true
		}
	}
	if !seenRead {
		t.Fatal("original read marker not shown")
	}
	// 首次归属查询故障沿用404；服务须包装lookup阶段且不执行后续写入。
	failure := errors.New("private-bill-failure")
	queryHook := fmt.Sprintf("bill-lookup-%d", id)
	if err := db.Callback().Query().Before("gorm:query").Register(queryHook, func(q *gorm.DB) {
		if q.Statement.Table == "charge_bill" {
			q.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(queryHook) })
	call("GET", path, 404)
	call("POST", path+"/read", 404)
	if err := service.MarkRead(context.Background(), 17, id); !errors.Is(err, settlement.ErrUserBillLookup) || !errors.Is(err, failure) {
		t.Fatalf("lookup stage/cause lost: %v", err)
	}
	db.Callback().Query().Remove(queryHook)
	// 已确认归属后已读保存故障沿用503；只影响当前fixture回调，不模拟资金失败。
	createHook := fmt.Sprintf("bill-read-write-%d", id)
	if err := db.Callback().Create().Before("gorm:create").Register(createHook, func(q *gorm.DB) {
		if q.Statement.Table == "charge_bill_read" {
			q.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Create().Remove(createHook) })
	newPath := fmt.Sprintf("/api/v1/user/bills/%d/read", id+2)
	call("POST", newPath, 503)
	db.Callback().Create().Remove(createHook)
	call("POST", newPath, 200)
	if err := tx.Table("charge_bill_read").Where("bill_id IN ?", []uint64{id, id + 1, id + 2}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("read recovery rows=%d: %v", count, err)
	}
}
