//go:build integration

package settlement

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	charging "wattharbor/internal/domain/charging"
	"wattharbor/internal/testfixture"
)

// oldFundingSnapshot 开启 RR 事务并普通读取测试记录以建立旧快照，调用方负责回滚。
func oldFundingSnapshot(t *testing.T, db *gorm.DB, table, column string, id uint64) *gorm.DB {
	t.Helper()
	tx := db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	var n int64
	if err := tx.Table(table).Where(column+"=?", id).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return tx
}

// TestManualRefundAndSecondSignatureSeeCommittedReceipts 验证其他连接提交申请和第一次审核后，旧快照内仍能重放申请并完成第二次审核。
// 首位审核人的权限由测试回调提供，仅清理本测试提交数据。
func TestManualRefundAndSecondSignatureSeeCommittedReceipts(t *testing.T) {
	writer, closeWriter := testConn(t)
	defer closeWriter()
	reader, closeReader := testConn(t)
	defer closeReader()
	cleanupRefundableOrder(t, writer)
	seedRefundableOrder(t, writer)
	request := uuid.NewString()
	defer func() {
		var ids []uint64
		if err := writer.Table("refund_record").Where("payment_order_id=?", refundPayOrdID).Pluck("id", &ids).Error; err != nil {
			t.Error(err)
		}
		if len(ids) > 0 {
			if err := writer.Table("refund_review").Where("refund_record_id IN ?", ids).Delete(nil).Error; err != nil {
				t.Error(err)
			}
		}
		if err := writer.Table("manual_refund_request").Where("request_id=?", request).Delete(nil).Error; err != nil {
			t.Error(err)
		}
		var n int64
		if err := writer.Table("manual_refund_request").Where("request_id=?", request).Count(&n).Error; err != nil || n != 0 {
			t.Errorf("remaining request=%d %v", n, err)
		}
		cleanupRefundableOrder(t, writer)
	}()
	ctx := context.Background()
	repo := RefundReviewRepo{LookupSigner: func(context.Context, uint64) (SignerStatus, error) {
		return SignerStatus{Role: "customer_finance", Permitted: true}, nil
	}}
	in := ManualRefundInput{ActorID: refundActorID, OrderID: refundOrderID, RequestID: request, AmountCents: 6000, Reason: "锁后原凭据"}
	old := oldFundingSnapshot(t, reader, "charge_order", "id", refundOrderID)
	defer old.Rollback()
	var no string
	if err := writer.Transaction(func(tx *gorm.DB) error {
		created, original, _, err := repo.ManualCreate(ctx, tx, in)
		if !created && err == nil {
			t.Error("first request was not created")
		}
		no = original
		return err
	}); err != nil {
		t.Fatal(err)
	}
	created, replay, _, err := repo.ManualCreate(ctx, old, in)
	if err != nil || created || replay != no {
		t.Fatalf("old snapshot replay created=%v no=%s err=%v", created, replay, err)
	}
	if err := old.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	old = oldFundingSnapshot(t, reader, "refund_record", "payment_order_id", refundPayOrdID)
	defer old.Rollback()
	if err := writer.Transaction(func(tx *gorm.DB) error {
		status, _, err := repo.Review(ctx, tx, no, ReviewDecision{Approve: true, ActorID: refundActorID, ApproveComment: "首签"})
		if err == nil && status != "awaiting_second" {
			t.Errorf("first status=%s", status)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, _, err := repo.Review(ctx, old, no, ReviewDecision{Approve: true, ActorID: refundActorID + 1, ApproveComment: "第二签"})
	if err != nil || status != "approved" {
		t.Fatalf("second signature status=%s err=%v", status, err)
	}
	if err := old.Rollback().Error; err != nil {
		t.Fatal(err)
	}
}

// TestWalletRefundReceiptAndLedgerUseCurrentFacts 覆盖成功回执重放及旧快照后的余额变动。
// 独立连接实际提交余额和退款；实际 executor 的锁、资金、流水及 Outbox 写入均走 MySQL。
func TestWalletRefundReceiptAndLedgerUseCurrentFacts(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "new_balance", true: "success_replay"}[replay], func(t *testing.T) {
			writer, closeWriter := testConn(t)
			defer closeWriter()
			reader, closeReader := testConn(t)
			defer closeReader()
			id := testfixture.NewID()
			no := "WS" + rand.Text()
			request := uuid.NewString()
			defer func() {
				for _, q := range []struct {
					table string // 本测试实际提交的表
					where string // 本测试唯一资金身份
					arg   any    // 查询筛选值
				}{
					{"event_outbox", "envelope_json LIKE ?", "%" + no + "%"}, {"wallet_txn", "user_id=?", id},
					{"refund_success_receipt", "refund_record_id=?", id}, {"wallet_refund_part", "refund_record_id=?", id},
					{"refund_record", "id=?", id}, {"payment_order", "id=?", id}, {"wallet_account", "id=?", id},
				} {
					var err error
					if q.table == "event_outbox" {
						err = testfixture.DeleteOutbox(writer, q.where, q.arg)
					} else {
						err = writer.Table(q.table).Where(q.where, q.arg).Delete(nil).Error
					}
					if err != nil {
						t.Error(err)
					}
					var n int64
					if err := writer.Table(q.table).Where(q.where, q.arg).Count(&n).Error; err != nil || n != 0 {
						t.Errorf("%s remaining=%d err=%v", q.table, n, err)
					}
				}
			}()
			for _, seed := range []struct {
				table string         // 钱包及退款来源所属表
				row   map[string]any // 资金身份及必需分值
			}{
				{"wallet_account", map[string]any{"id": id, "user_id": id, "balance_cents": 1000, "frozen_cents": 400, "version": 1}},
				{"payment_order", map[string]any{"id": id, "order_no": "P-" + no, "user_id": id, "biz_type": "wallet_recharge", "biz_id": 0, "pay_method": "wechat", "status": "paid", "total_cents": 1000, "paid_cents": 1000, "wechat_transaction_id": "TX-" + no, "created_month": "2026-10-01"}},
				{"refund_record", map[string]any{"id": id, "refund_no": no, "payment_order_id": id, "user_id": id, "biz_type": "wallet_recharge", "biz_id": 0, "refund_cents": 400, "status": "processing", "execution_policy": "automatic", "created_month": "2026-10-01"}},
				{"wallet_refund_part", map[string]any{"refund_record_id": id, "request_id": request, "wallet_account_id": id, "amount_cents": 400}},
			} {
				if err := writer.Table(seed.table).Create(seed.row).Error; err != nil {
					t.Fatal(err)
				}
			}
			old := oldFundingSnapshot(t, reader, "wallet_account", "id", id)
			defer old.Rollback()
			r := RefundRecord{ID: id, RefundNo: no, PaymentOrderID: id, UserID: id, BizType: "wallet_recharge", RefundCents: 400, Status: "processing"}
			req := execRequest(no, 400)
			req.MerchantOrderNo = "P-" + no
			req.TransactionID = "TX-" + no
			req.TotalCents = 1000
			result := execResult(no, 400, "WR-"+no)
			result.MerchantOrderNo = req.MerchantOrderNo
			result.TransactionID = req.TransactionID
			result.TotalCents = 1000
			if replay {
				if err := (RefundExecutor{DB: writer}).apply(context.Background(), r, req, result); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := writer.Table("wallet_account").Where("id=?", id).Updates(map[string]any{"balance_cents": 1200, "frozen_cents": 600, "version": 2}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := (RefundExecutor{DB: old}).apply(context.Background(), r, req, result); err != nil {
				t.Fatalf("old snapshot apply: %v", err)
			}
			if err := old.Commit().Error; err != nil {
				t.Fatal(err)
			}
			var row struct {
				BalanceCents int64  // 当前钱包余额，分
				FrozenCents  int64  // 当前预占余额，分
				Version      uint64 // 每次已提交资金变更的版本
			}
			if err := writer.Table("wallet_account").Where("id=?", id).Take(&row).Error; err != nil {
				t.Fatal(err)
			}
			wantBalance, wantFrozen, wantVersion := int64(800), int64(200), uint64(3)
			if replay {
				wantBalance, wantFrozen, wantVersion = 600, 0, 2
			}
			var ledger int64
			if err := writer.Table("wallet_txn").Where("user_id=?", id).Pluck("balance_after_cents", &ledger).Error; err != nil {
				t.Fatal(err)
			}
			if row.BalanceCents != wantBalance || row.FrozenCents != wantFrozen || row.Version != wantVersion || ledger != wantBalance {
				t.Fatalf("wallet=%+v ledger=%d want=%d/%d/v%d", row, ledger, wantBalance, wantFrozen, wantVersion)
			}
		})
	}
}

// TestBillingApplyReplaysAfterOldSnapshot 验证支付锁前建立旧快照后，仍能看到已提交的结算回执。
// 价格、计量、费用和余款退款由其他连接提交；重试仅补齐任务确认，不重复费用、退款或事件。
func TestBillingApplyReplaysAfterOldSnapshot(t *testing.T) {
	writer, closeWriter := testConn(t)
	defer closeWriter()
	reader, closeReader := testConn(t)
	defer closeReader()
	id := testfixture.NewID()
	no := "C-BR-" + rand.Text()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	rule := charging.Rule{ID: 1, StationID: 1, Version: 1, Spec: charging.Spec{Scheme: &charging.Scheme{}}}
	contract, _ := json.Marshal(map[string]any{"rule": rule, "offer": charging.Offer{ID: 1, PriceCents: 10000}})
	meter, _ := json.Marshal(charging.EndMeter{EndedAt: at, ChargedWh: 5000, ChargedSeconds: 3600})
	defer func() {
		for _, q := range []struct {
			table string // 本测试实际提交的表
			where string // 本测试唯一结算身份
			arg   any    // 查询筛选值
		}{
			{"event_outbox", "envelope_json LIKE ?", "%" + no + "%"},
			{"event_outbox", "envelope_json LIKE ?", "%FEE-R" + fmt.Sprintf("%020d", id) + "%"},
			{"refund_record", "payment_order_id=?", id}, {"charge_fee_receipt", "charge_order_id=?", id},
			{"charge_event_log", "charge_order_id=?", id}, {"charge_manual_settlement", "charge_order_id=?", id},
			{"charge_billing_job", "charge_order_id=?", id}, {"charge_end_receipt", "charge_order_id=?", id},
			{"charge_order_pricing", "charge_order_id=?", id}, {"charge_order", "id=?", id}, {"payment_order", "id=?", id},
		} {
			var err error
			if q.table == "event_outbox" {
				err = testfixture.DeleteOutbox(writer, q.where, q.arg)
			} else {
				err = writer.Table(q.table).Where(q.where, q.arg).Delete(nil).Error
			}
			if err != nil {
				t.Error(err)
			}
			var n int64
			if err := writer.Table(q.table).Where(q.where, q.arg).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("%s remaining=%d %v", q.table, n, err)
			}
		}
	}()
	for _, seed := range []struct {
		table string         // 同一订单及其价格、计量快照所在表
		row   map[string]any // 身份、时刻及人工最终费用
	}{
		{"payment_order", map[string]any{"id": id, "order_no": "P-" + no, "user_id": id, "biz_type": "charge", "biz_id": id, "pay_method": "wechat", "status": "paid", "total_cents": 10000, "paid_cents": 10000, "created_month": "2026-10-01"}},
		{"charge_order", map[string]any{"id": id, "order_no": no, "user_id": id, "device_id": no, "port_no": 1, "status": "completed", "payment_status": "paid", "payment_order_id": id, "started_at": at.Add(-time.Hour), "ended_at": at, "created_month": "2026-10-01"}},
		{"charge_order_pricing", map[string]any{"charge_order_id": id, "payment_intent_id": uuid.NewString(), "user_id": id, "port_code": no, "pricing_snapshot": string(contract)}},
		{"charge_end_receipt", map[string]any{"charge_order_id": id, "stop_command_id": uuid.NewString(), "meter_json": string(meter)}},
		{"charge_manual_settlement", map[string]any{"charge_order_id": id, "request_id": uuid.NewString(), "actor_id": id, "electric_cents": 300, "service_cents": 100, "reason": "proof"}},
		{"charge_billing_job", map[string]any{"charge_order_id": id, "status": "pending"}},
	} {
		if err := writer.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(err)
		}
	}
	source, err := (BillingOrders{DB: writer}).Read(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	fee, err := PriceSource(source)
	if err != nil {
		t.Fatal(err)
	}
	result := Result{Source: source, CalculationNo: CalculationNumber(no, id), ActualFee: fee}
	old := oldFundingSnapshot(t, reader, "payment_order", "id", id)
	defer old.Rollback()
	if err := (BillingOrders{DB: writer}).Apply(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if err := (BillingOrders{DB: old}).Apply(context.Background(), result); err != nil {
		t.Fatalf("old snapshot billing replay: %v", err)
	}
	if err := old.Commit().Error; err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		table string /* 入账所属表 */
		where string /* 同一订单或支付身份 */
	}{{"charge_fee_receipt", "charge_order_id=?"}, {"refund_record", "payment_order_id=?"}, {"charge_event_log", "charge_order_id=?"}} {
		var n int64
		if err := writer.Table(q.table).Where(q.where, id).Count(&n).Error; err != nil || n != 1 {
			t.Errorf("%s count=%d %v", q.table, n, err)
		}
	}
}
