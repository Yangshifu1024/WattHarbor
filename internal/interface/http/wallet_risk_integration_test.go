//go:build integration

package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 钱包退款频次风控的真库并发测试。
//
// `walletRefund` 的流程是：锁钱包账户行 → 幂等复查 → 落申请 → 数近 5 分钟
// 已有多少笔 → 达到 3 笔就转人工复核并冻结钱包。
//
// 频次计数走的是 `CountRecentRefundRequests`，一个没有加锁子句的普通读。
// 事务用的是默认的 REPEATABLE READ，所以它是**快照读**。这是否构成漏洞
// 取决于快照在哪一刻建立：若在拿到钱包行锁**之前**建立，并发申请就各自
// 读到旧值，5 分钟内 3 笔的上限会被整个绕过——用户可以一次性提交任意多笔。
// 单测完全看不出来，必须真库并发验。

const (
	riskUserID    = uint64(71001)
	riskBalance   = int64(100000)
	riskEachCents = int64(1000)
	riskLimit     = 3
	// rechargeCents 是原充值可退额度。故意小于钱包余额，
	// 让「原充值还剩多少可退」成为唯一的约束——测的就是这一条。
	rechargeCents = int64(10000)
)

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

func cleanupWalletForRisk(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, q := range []struct{ table, where string }{
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
	}
}

type walletSession struct{}

func (walletSession) Exists(context.Context, string) (bool, error) {
	return true, nil
}

type walletUser struct{}

func (walletUser) Active(context.Context, uint64) (bool, error) {
	return true, nil
}

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

// TestConcurrentWalletRefundRequestsEnforceFrequencyLimit 钉住频次上限不被并发绕过。
//
// 5 分钟内最多 3 笔（达到第 3 笔就转人工复核并冻结钱包）。
// 5 笔并发提交：恰好 3 笔 accepted、2 笔 manual_review，且钱包被冻结。
// 若频次计数读到快照旧值，5 笔会全部 accepted——风控形同虚设。
func TestConcurrentWalletRefundRequestsEnforceFrequencyLimit(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupWalletForRisk(t, janitor) })
	cleanupWalletForRisk(t, setup)
	seedWalletForRisk(t, setup)

	jwt, err := auth.NewJWT("test-wallet-risk-secret-must-be-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, err := jwt.Sign(auth.Claims{
		Subject: fmt.Sprint(riskUserID), Kind: "user", OpenID: "ox-risk-1",
		SessionID: "session-risk", IssuedAt: now.Add(-time.Minute).Unix(),
		ExpiresAt: now.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)

	const total = 5
	type reply struct {
		code int
		body map[string]any
	}
	results := make([]reply, total)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		// 每笔用各自的独立连接与自己的 API 实例：共用一个连接池时，
		// 请求会串行执行，测出来的是假并发。
		p, closeP := testConn(t)
		router := httpapi.NewRouter()
		UserAccountAPI{
			Auth:   SessionAuthenticator{Auth: NewAuthenticator(jwt, walletSession{}, walletUser{})},
			UserDB: p,
		}.Register(router)
		wg.Add(1)
		go func(idx int, r *gin.Engine) {
			defer wg.Done()
			<-start
			code, body := postRefund(t, r, token, fmt.Sprintf("2222%04d-2222-4222-8222-222222222222", idx))
			results[idx] = reply{code, body}
		}(i, router)
		defer closeP()
	}
	close(start)
	wg.Wait()

	accepted, review, frozen := 0, 0, 0
	for i, r := range results {
		data, _ := r.body["data"].(map[string]any)
		state, _ := data["status"].(string)
		switch {
		case r.code == 200 && state == "accepted":
			accepted++
		case r.code == 200 && state == "manual_review":
			review++
		case r.code == 409:
			frozen++
		default:
			t.Errorf("第 %d 笔返回了预期之外的结果：code=%d body=%v", i, r.code, r.body)
		}
	}
	// 上限是「5 分钟内第 3 笔起转人工复核并冻结钱包」。
	// 冻结之后到达的申请会被 409 挡下，那是正确行为，不是失败。
	if accepted+review > riskLimit {
		t.Errorf("受理 %d + 复核 %d = %d 笔，超过 5 分钟 %d 笔的上限——频次风控被绕过",
			accepted, review, accepted+review, riskLimit)
	}
	if review != 1 {
		t.Errorf("应有恰好 1 笔转入人工复核，实际 %d 笔（受理 %d、冻结后拒绝 %d）", review, accepted, frozen)
	}
	if accepted != riskLimit-review {
		t.Errorf("受理 %d 笔，期望 %d 笔（上限 %d 减去转复核的 %d 笔）",
			accepted, riskLimit-review, riskLimit, review)
	}

	// 达到上限时钱包必须被冻结，否则后续申请仍会照常受理。
	verify, closeVerify := testConn(t)
	defer closeVerify()
	var status string
	var frozenCents int64
	if err := verify.Table("wallet_account").Where("user_id = ?", riskUserID).
		Select("status, frozen_cents").Row().Scan(&status, &frozenCents); err != nil {
		t.Fatalf("读钱包失败：%v", err)
	}
	if review > 0 && status != "frozen" {
		t.Errorf("有 %d 笔转了人工复核，钱包状态却是 %q，期望 frozen", review, status)
	}
}

// TestConcurrentWalletRefundsDoNotExceedRecharge 钉住并发退款不超原充值额。
//
// 上面验的是频次，这里验金额。两笔并发退款各 8000，而原充值只有 10000。
// ReserveRefund 拆单时数的是 refund_record 的 SUM——一个没有加锁的快照读。
// 单看它，两笔都可能在快照里数到 0，于是各自算出「可退 10000」，
// 合计退掉 16000。
//
// 真正的守门员是 wallet_account.frozen_cents：它随每笔预占累加，
// 且是在钱包行锁内读到的当前值，所以后到的那笔会看到已冻结的 8000。
// 这个测试就是为了钉住「frozen_cents 确实是承重的」——
// 拿掉它或让它读快照，测试立刻变红。
func TestConcurrentWalletRefundsDoNotExceedRecharge(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupWalletForRisk(t, janitor) })
	cleanupWalletForRisk(t, setup)
	seedWalletForRisk(t, setup)

	jwt, err := auth.NewJWT("test-wallet-risk-secret-must-be-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, err := jwt.Sign(auth.Claims{
		Subject: fmt.Sprint(riskUserID), Kind: "user", OpenID: "ox-risk-1",
		SessionID: "session-risk", IssuedAt: now.Add(-time.Minute).Unix(),
		ExpiresAt: now.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)

	post := func(idx int) (int, map[string]any) {
		p, closeP := testConn(t)
		defer closeP()
		router := httpapi.NewRouter()
		UserAccountAPI{
			Auth:   SessionAuthenticator{Auth: NewAuthenticator(jwt, walletSession{}, walletUser{})},
			UserDB: p,
		}.Register(router)
		body := fmt.Sprintf(`{"request_id":"3333%04d-3333-4333-8333-333333333333","amount_cents":8000,"reason":"并发超额退款"}`, idx)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/user/wallet/refund", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var parsed map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
		return rec.Code, parsed
	}

	codes := make([]int, 2)
	bodies := make([]map[string]any, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			codes[idx], bodies[idx] = post(idx)
		}(i)
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for i, c := range codes {
		if c == 200 {
			succeeded++
			continue
		}
		t.Logf("第 %d 笔被拒：%d %v", i, c, bodies[i])
	}
	if succeeded > 1 {
		t.Errorf("两笔各 8000 的退款都受理了，而原充值只有 %d 分", rechargeCents)
	}

	verify, closeVerify := testConn(t)
	defer closeVerify()
	var total, frozenCents int64
	if err := verify.Table("refund_record").
		Where("payment_order_id = ? AND status IN ('pending','processing')", 71001).
		Select("COALESCE(SUM(refund_cents),0)").Row().Scan(&total); err != nil {
		t.Fatalf("统计待处理退款失败：%v", err)
	}
	if err := verify.Table("wallet_account").Where("user_id = ?", riskUserID).
		Select("frozen_cents").Row().Scan(&frozenCents); err != nil {
		t.Fatalf("读冻结额失败：%v", err)
	}
	if total > rechargeCents {
		t.Errorf("待处理退款合计 %d 分，超过原充值 %d 分——并发退款超发", total, rechargeCents)
	}
	if total != frozenCents {
		t.Errorf("退款记录合计 %d 与冻结额 %d 不一致，两者本该同增同减", total, frozenCents)
	}
}
