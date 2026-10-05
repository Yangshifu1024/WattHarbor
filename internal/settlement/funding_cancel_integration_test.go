//go:build integration

package settlement

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/testfixture"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestRechargeCancellationAfterBalanceWriteRollsBackAllFunds 在真实余额 UPDATE 完成后取消原回调请求。
// 使用用例管理的顶层事务，验证支付、摘要、钱包及流水整体回滚；未请求渠道，造数显式清理为零。
func TestRechargeCancellationAfterBalanceWriteRollsBackAllFunds(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	id := testfixture.NewID()
	request := uuid.NewString()
	no := "CANCEL" + rand.Text()
	openid := "cancel-" + rand.Text()
	transaction := "TX-" + no
	defer func() {
		for _, q := range []struct {
			table string // 已提交测试数据所属表
			where string // 当前测试独占的身份筛选
			arg   any    // 与筛选对应的用户、请求或通知身份
		}{{"event_outbox", "envelope_json LIKE ?", "%" + request + "%"}, {"wallet_txn", "user_id=?", id}, {"payment_callback_idempotent", "wechat_transaction_id=?", transaction}, {"wallet_recharge_request", "request_id=?", request}, {"payment_order", "id=?", id}, {"wallet_account", "id=?", id}, {"user", "id=?", id}} {
			var err error
			if q.table == "event_outbox" {
				err = testfixture.DeleteOutbox(db, q.where, q.arg)
			} else {
				err = db.Table(q.table).Where(q.where, q.arg).Delete(nil).Error
			}
			if err != nil {
				t.Error(err)
			}
			var n int64
			if err := db.Table(q.table).Where(q.where, q.arg).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("%s remaining=%d %v", q.table, n, err)
			}
		}
	}()
	for _, row := range []struct {
		table  string         // 原用户、资金与申请表
		values map[string]any // 原充值申请必需事实
	}{{"user", map[string]any{"id": id, "openid": openid, "status": "active"}}, {"wallet_account", map[string]any{"id": id, "user_id": id, "balance_cents": 100, "frozen_cents": 0, "version": 1, "status": "frozen"}}, {"payment_order", map[string]any{"id": id, "order_no": no, "biz_type": "wallet_recharge", "biz_id": 0, "user_id": id, "pay_method": "wechat", "total_cents": 500, "paid_cents": 0, "status": "initiated", "created_month": time.Now().UTC().Format("2006-01") + "-01"}}, {"wallet_recharge_request", map[string]any{"request_id": request, "user_id": id, "amount_cents": 500, "payment_order_id": id}}} {
		if err := db.Table(row.table).Create(row.values).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := false
	key := "cancel_credit_" + rand.Text()
	if err := db.Callback().Update().After("gorm:update").Register(key, func(q *gorm.DB) {
		if q.Statement.Table == "wallet_account" && q.Error == nil {
			cancelled = true
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Update().Remove(key)
	_, err := (payment.CallbackService{DB: db, ExpectedProvider: "wechat_direct", ExpectedMerchantID: "merchant", ExpectedAppID: "app"}).Apply(ctx, payment.VerifiedPayment{Provider: "wechat_direct", MerchantID: "merchant", AppID: "app", MerchantOrderNo: no, TransactionID: transaction, OpenID: openid, PaidCents: 500, PaidAt: time.Now().UTC()})
	if !cancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("balance write cancellation not reached: cancelled=%t err=%v", cancelled, err)
	}
	var w wallet.WalletRow
	if err := db.Table("wallet_account").Where("id=?", id).Take(&w).Error; err != nil || w.BalanceCents != 100 || w.Version != 1 {
		t.Fatalf("wallet committed after cancellation: %+v %v", w, err)
	}
	var pay payment.PaymentOrderRecord
	if err := db.Where("id=?", id).Take(&pay).Error; err != nil || pay.Status != "initiated" || pay.PaidCents != 0 {
		t.Fatalf("payment committed after cancellation: %+v %v", pay, err)
	}
	for _, q := range []struct {
		table string // 本次回调不应留下的凭据
		where string // 通知、用户或申请身份筛选
		arg   any    // 当前测试身份
	}{{"payment_callback_idempotent", "wechat_transaction_id=?", transaction}, {"wallet_txn", "user_id=?", id}, {"event_outbox", "envelope_json LIKE ?", "%" + request + "%"}} {
		var n int64
		if err := db.Table(q.table).Where(q.where, q.arg).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("%s survived cancellation=%d %v", q.table, n, err)
		}
	}
}
