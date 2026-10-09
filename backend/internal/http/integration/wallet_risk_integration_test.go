//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	authhttp "chargepilot/internal/http/auth"
	userwallet "chargepilot/internal/http/user/wallet"
	"chargepilot/internal/payment"
	"chargepilot/internal/platform/auth"
	"chargepilot/internal/platform/httpapi"
	"chargepilot/internal/settlement"
	"chargepilot/internal/wallet"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 钱包退款的注册 HTTP 并发验收使用两条独立 MySQL 连接。
// 首请求取得 wallet_account 行锁后挂起；第二请求到达同表锁查询后放行首请求。
// 频次查询在取得钱包锁后首次建立快照，来源退款额度由当前读取核对。

const (
	riskUserID    = uint64(71001)
	riskBalance   = int64(100000)
	riskEachCents = int64(1000)
	riskLimit     = 3
	// rechargeCents 是原充值可退额度。故意小于钱包余额，
	// 让「原充值还剩多少可退」成为唯一的约束——测的就是这一条。
	rechargeCents = int64(10000)
)

// seedWalletForRisk 在 db 写入测试钱包和已支付充值单，t 报告造数失败；没有返回值，仅供独立真库并发退款验证。
func seedWalletForRisk(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Table("wallet_account").Create(map[string]any{
		"id": 71001, "user_id": riskUserID, "balance_cents": riskBalance, "frozen_cents": 0,
		"status": "active", "version": 1,
	}).Error; err != nil {
		t.Fatalf("造钱包账户失败：%v", err)
	}
	// ReserveRefund 按「原充值可退额度」核销：只认 biz_type=wallet_recharge、
	// status=paid、paid=total、带渠道流水号的支付单。缺了它每笔都判冲突。
	if err := db.Table("payment_order").Create(map[string]any{
		"id": 71001, "order_no": "P-WALLET-RISK", "biz_type": "wallet_recharge", "biz_id": 0,
		"user_id": riskUserID, "pay_method": "wechat", "total_cents": rechargeCents,
		"paid_cents": rechargeCents, "refunded_cents": 0, "status": "paid",
		"wechat_transaction_id": "SIM71001", "created_month": "2026-03-01",
	}).Error; err != nil {
		t.Fatalf("造充值支付单失败：%v", err)
	}
}

// cleanupWalletForRisk 从 db 删除本测试用户及指定请求号前缀的数据并核对剩余行数，t 报告清理错误；没有返回值，不用于开发库。
func cleanupWalletForRisk(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, q := range []struct {
		table, where string // 本测试数据清理的表名与参数化条件。
	}{
		{"wallet_refund_part", "wallet_account_id = ?"},
		{"wallet_refund_request", "user_id = ?"},
		{"wallet_risk_freeze_link", "request_id LIKE ?"},
		{"risk_freeze_log", "user_id = ?"},
		{"wallet_account", "user_id = ?"},
		{"payment_order", "user_id = ?"},
		{"refund_record", "user_id = ?"},
	} {
		arg := any(riskUserID)
		switch q.table {
		case "wallet_refund_part":
			arg = 71001
		case "wallet_risk_freeze_link":
			// 该表以 request_id 为主键，没有 user_id 列，只能按本测试的 UUID 前缀清。
			arg = "22%"
		}
		if err := db.Table(q.table).Where(q.where, arg).Delete(nil).Error; err != nil {
			t.Errorf("清理 %s 失败：%v", q.table, err)
		}
		var left int64
		if err := db.Table(q.table).Where(q.where, arg).Count(&left).Error; err != nil || left != 0 {
			t.Errorf("清理 %s 后残留=%d err=%v", q.table, left, err)
		}
	}
}

// walletSession 是钱包路由测试的固定有效会话能力，用于隔离 Redis；真实 JWT 验证仍由注册的认证器执行。
type walletSession struct{}

// Exists 忽略输入 context 和 SID，固定返回 true、nil 以隔离钱包业务测试；不访问 Redis，不能证明真实会话认证。
func (walletSession) Exists(context.Context, string) (bool, error) {
	return true, nil
}

// walletUser 是钱包路由测试的固定有效用户能力，用于隔离账号状态读取；不证明真实用户状态已通过数据库校验。
type walletUser struct{}

// Active 忽略输入 context 和用户编号，固定返回 true、nil 以隔离钱包业务测试；不访问数据库，不能证明真实用户状态认证。
func (walletUser) Active(context.Context, uint64) (bool, error) {
	return true, nil
}

// postRefund 用 router、token 和 requestID 提交固定分值的退款 JSON，t 标记辅助调用，返回 HTTP 状态与解析后的信封。
// 请求执行业务路由及所属真库用例；仅供测试，信封解析错误沿用原助手处理。
func postRefund(t *testing.T, router *gin.Engine, token, requestID string) (int, map[string]any) {
	t.Helper()
	body := fmt.Sprintf(`{"request_id":%q,"amount_cents":%d,"reason":"并发频次测试"}`, requestID, riskEachCents)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/wallet/refund", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed
}

// concurrentWalletReplies 在实际注册路由上用 first、second 连接执行两个退款请求。
// t 报告闸门或路由错误，token 提供测试用户身份，amount 为每笔分值，prefix 为隔离请求号。
// 首请求锁定钱包后启动第二请求，确认第二请求到达钱包锁再放行；返回两份按启动顺序的回执。
func concurrentWalletReplies(t *testing.T, first, second *gorm.DB, jwt *auth.JWT, token string, amount int64, prefix string) [2]walletRouteReply {
	t.Helper()
	locked, release, attempting := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var lockOnce, releaseOnce, attemptOnce sync.Once
	pass := func() { releaseOnce.Do(func() { close(release) }) }
	defer pass()
	firstName, secondName := "test:wallet_first_"+prefix, "test:wallet_second_"+prefix
	if err := first.Callback().Query().After("gorm:query").Register(firstName, func(q *gorm.DB) {
		sql := q.Statement.SQL.String()
		if !strings.Contains(sql, "FROM `wallet_account`") || !strings.Contains(sql, "FOR UPDATE") {
			return
		}
		lockOnce.Do(func() { close(locked); <-release })
	}); err != nil {
		t.Fatal(err)
	}
	defer first.Callback().Query().Remove(firstName)
	if err := second.Callback().Query().Before("gorm:query").Register(secondName, func(q *gorm.DB) {
		if q.Statement.Table != "wallet_account" {
			return
		}
		if _, ok := q.Statement.Clauses["FOR"]; !ok {
			return
		}
		attemptOnce.Do(func() { close(attempting) })
	}); err != nil {
		t.Fatal(err)
	}
	defer second.Callback().Query().Remove(secondName)
	send := func(db *gorm.DB, idx int) <-chan walletRouteReply {
		ch := make(chan walletRouteReply, 1)
		router := httpapi.NewRouter()
		userwallet.API{WalletRefund: wallet.RefundService{DB: db, Sources: payment.RechargeSources{}, Recorder: settlement.RefundRecorder{}}, Auth: authhttp.SessionAuthenticator{Auth: authhttp.NewAuthenticator(jwt, walletSession{}, walletUser{})}}.Register(router)
		go func() {
			body := fmt.Sprintf(`{"request_id":"%s%04d-3333-4333-8333-333333333333","amount_cents":%d,"reason":"确定顺序并发"}`, prefix, idx, amount)
			r := httptest.NewRequest("POST", "/api/v1/user/wallet/refund", bytes.NewBufferString(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			var payload map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &payload)
			ch <- walletRouteReply{code: w.Code, body: payload}
		}()
		return ch
	}
	wait := func(ch <-chan struct{}, name string) {
		select {
		case <-ch:
		case <-time.After(15 * time.Second):
			t.Fatalf("%s 未到达指定表锁", name)
		}
	}
	firstResult := send(first, 0)
	wait(locked, "首请求")
	secondResult := send(second, 1)
	wait(attempting, "次请求")
	pass()
	var out [2]walletRouteReply
	for i, ch := range []<-chan walletRouteReply{firstResult, secondResult} {
		select {
		case out[i] = <-ch:
		case <-time.After(15 * time.Second):
			t.Fatal("退款请求未在期限内结束")
		}
	}
	return out
}

// walletRouteReply 保留并发请求的状态码与原业务 JSON，按启动顺序核对结果。
type walletRouteReply struct {
	code int            // 实际注册 HTTP 状态码
	body map[string]any // 原业务响应信封
}

// walletRiskToken 用 jwt 为固定测试用户签发会话令牌；t 报告错误，不调用真实身份服务。
func walletRiskToken(t *testing.T, jwt *auth.JWT) string {
	t.Helper()
	now := time.Now()
	token, err := jwt.Sign(auth.Claims{Subject: fmt.Sprint(riskUserID), Kind: "user", OpenID: "ox-risk-1", SessionID: "session-risk", IssuedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// TestConcurrentWalletRefundRequestsEnforceFrequencyLimit 确认第三次申请冻结钱包，等待中的第四次按当前状态拒绝。
// 两条独立连接与明确表锁闸门控制顺序；真实提交后按固定测试身份清理，并核对零残留。
func TestConcurrentWalletRefundRequestsEnforceFrequencyLimit(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupWalletForRisk(t, janitor) })
	cleanupWalletForRisk(t, setup)
	seedWalletForRisk(t, setup)
	for i := 0; i < 2; i++ {
		if err := setup.Table("wallet_refund_request").Create(map[string]any{"request_id": fmt.Sprintf("2222999%d-3333-4333-8333-333333333333", i), "user_id": riskUserID, "amount_cents": 100, "reason": "既有频次", "response_json": "{}"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	first, closeFirst := testConn(t)
	defer closeFirst()
	second, closeSecond := testConn(t)
	defer closeSecond()
	jwt, err := auth.NewJWT("test-wallet-risk-secret-must-be-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	results := concurrentWalletReplies(t, first, second, jwt, walletRiskToken(t, jwt), riskEachCents, "2222")
	data, _ := results[0].body["data"].(map[string]any)
	if results[0].code != 200 || data["status"] != "manual_review" || results[1].code != 409 {
		t.Fatalf("频次回执=%+v", results)
	}
	var status string
	var count int64
	if err := setup.Table("wallet_account").Where("user_id=?", riskUserID).Pluck("status", &status).Error; err != nil {
		t.Fatal(err)
	}
	if err := setup.Table("wallet_refund_request").Where("user_id=?", riskUserID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if status != "frozen" || count != riskLimit {
		t.Fatalf("频次未保护：status=%s requests=%d", status, count)
	}
}

// TestConcurrentWalletRefundsDoNotExceedRecharge 确认首笔 8000 分提交后，次笔不能占用同充值的剩余 2000 分。
// 钱包余额高于原渠道来源，以便由在途退款当前读而非余额总额决定第二笔拒绝；清理已提交的真实行。
func TestConcurrentWalletRefundsDoNotExceedRecharge(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupWalletForRisk(t, janitor) })
	cleanupWalletForRisk(t, setup)
	seedWalletForRisk(t, setup)
	first, closeFirst := testConn(t)
	defer closeFirst()
	second, closeSecond := testConn(t)
	defer closeSecond()
	jwt, err := auth.NewJWT("test-wallet-risk-secret-must-be-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	results := concurrentWalletReplies(t, first, second, jwt, walletRiskToken(t, jwt), 8000, "3333")
	if results[0].code != 200 || results[1].code != 409 {
		t.Fatalf("来源额度回执=%+v", results)
	}
	var total, frozen int64
	if err := setup.Table("refund_record").Where("payment_order_id=? AND status IN ('pending','processing')", 71001).Select("COALESCE(SUM(refund_cents),0)").Row().Scan(&total); err != nil {
		t.Fatal(err)
	}
	if err := setup.Table("wallet_account").Where("user_id=?", riskUserID).Pluck("frozen_cents", &frozen).Error; err != nil {
		t.Fatal(err)
	}
	if total != 8000 || frozen != total {
		t.Fatalf("预占不一致：refund=%d frozen=%d", total, frozen)
	}
}
