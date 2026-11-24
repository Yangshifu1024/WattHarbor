//go:build integration

package settlement

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm/clause"
	"testing"
	"time"
	"wattharbor/internal/domain/money"
	pay "wattharbor/internal/payment"
)

// guardRefundProvider 控制渠道缺单或已有成功回执；只验证执行边界，不代表真实渠道。
type guardRefundProvider struct {
	missing          bool
	queries, creates int
}

func (p *guardRefundProvider) QueryRefund(_ context.Context, request pay.RefundRequest) (pay.RefundResult, error) {
	p.queries++
	if p.missing {
		return pay.RefundResult{}, pay.ErrRefundNotFound
	}
	return guardRefundResult(request, pay.ChannelRefundStatusSuccess), nil
}

func (p *guardRefundProvider) CreateRefund(_ context.Context, request pay.RefundRequest) (pay.RefundResult, error) {
	p.creates++
	return guardRefundResult(request, pay.ChannelRefundStatusProcessing), nil
}

func guardRefundResult(request pay.RefundRequest, status pay.ChannelRefundStatus) pay.RefundResult {
	return pay.RefundResult{RefundNo: request.RefundNo, MerchantOrderNo: request.MerchantOrderNo,
		TransactionID: request.TransactionID, TotalCents: request.TotalCents, RefundCents: request.RefundCents,
		RefundID: "WX-" + request.RefundNo, Status: status, SuccessAt: time.Now().UTC()}
}

// TestHistoricalApprovedRefundWaitsForSettlement 真实表验证升级前批准单：缺单不能新发起，已有成功必须入账。
func TestHistoricalApprovedRefundWaitsForSettlement(t *testing.T) {
	for _, scenario := range []struct {
		name, orderStatus, refundStatus string
		missing, blocked                bool
	}{
		{"pending_completed", "completed", "pending", true, true},
		{"automatic_completed", "completed", "pending", true, true},
		{"processing_completed", "completed", "processing", true, true},
		{"processing_refunding", "refunding", "processing", true, true},
		{"existing_success", "completed", "processing", false, false},
		{"failed", "failed", "pending", true, false},
		{"cancelled", "cancelled", "pending", true, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, closeDB := testConn(t)
			defer closeDB()
			if scenario.name == "pending_completed" {
				// 先提交无关申请再锁定，避免夹具插入被主事务的缺键间隙锁阻塞。
				other, closeOther := testConn(t)
				defer closeOther()
				requestID := "00000000-0000-0000-0000-" + uuid.NewString()[24:]
				if err := other.Table("manual_refund_request").Create(map[string]any{"request_id": requestID, "refund_no": "UNRELATED-" + uuid.NewString(), "payload_json": `{}`}).Error; err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := other.Table("manual_refund_request").Where("request_id=?", requestID).Delete(nil).Error; err != nil {
						t.Error(err)
					}
					var n int64
					if err := other.Table("manual_refund_request").Where("request_id=?", requestID).Count(&n).Error; err != nil || n != 0 {
						t.Errorf("unrelated request residual=%d err=%v", n, err)
					}
				}()
				hold := other.Begin()
				if hold.Error != nil {
					t.Fatal(hold.Error)
				}
				defer hold.Rollback()
				var locked struct{ RequestID string }
				if err := hold.Table("manual_refund_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id=?", requestID).Take(&locked).Error; err != nil {
					t.Fatal(err)
				}
			}
			tx := db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			seedRefundableOrder(t, tx)
			created, no, id, err := (RefundReviewRepo{}).ManualCreate(t.Context(), tx, ManualRefundInput{
				ActorID: refundActorID, OrderID: refundOrderID, RequestID: "historical-" + scenario.name, AmountCents: 100, Reason: "历史退款",
			})
			if err != nil || !created {
				t.Fatalf("seed created=%v err=%v", created, err)
			}
			if scenario.name == "automatic_completed" {
				if err := tx.Table("manual_refund_request").Where("refund_no=?", no).Delete(nil).Error; err != nil {
					t.Fatal(err)
				}
			}
			for _, change := range []struct {
				table, where string
				values       map[string]any
			}{
				{"charge_order", "id", map[string]any{"status": scenario.orderStatus}},
				{"refund_record", "id", map[string]any{"status": scenario.refundStatus, "execution_policy": "automatic", "next_attempt_at": time.Now().Add(-time.Minute).UTC()}},
			} {
				key := refundOrderID
				if change.table == "refund_record" {
					key = id
				}
				if err := tx.Table(change.table).Where(change.where+"=?", key).Updates(change.values).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Table("charge_fee_receipt").Where("charge_order_id=?", refundOrderID).Delete(nil).Error; err != nil {
				t.Fatal(err)
			}
			if err := tx.Table("charge_prepay").Create(map[string]any{"payment_order_id": refundPayOrdID, "params_json": `{"provider":"wechat"}`}).Error; err != nil {
				t.Fatal(err)
			}
			provider := &guardRefundProvider{missing: scenario.missing}
			executor := RefundExecutor{DB: tx, Provider: provider, ProviderName: "wechat"}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			err = executor.Execute(ctx, id)
			if scenario.blocked {
				if !errors.Is(err, ErrRefundNotSettled) || provider.queries != 1 || provider.creates != 0 {
					t.Fatalf("err=%v provider=%+v", err, provider)
				}
				var record RefundRecord
				if err := tx.Where("id=?", id).Take(&record).Error; err != nil {
					t.Fatal(err)
				}
				if record.Status != money.RefundProcessing || record.RefundNo != no {
					t.Fatalf("changed identity/state: %+v", record)
				}
				if err := tx.Table("charge_fee_receipt").Create(map[string]any{"charge_order_id": refundOrderID, "calculation_no": "CALC-RESTORED", "result_json": `{"total_cents":10000}`}).Error; err != nil {
					t.Fatal(err)
				}
				if err := tx.Table("refund_record").Where("id=?", id).Update("next_attempt_at", time.Now().Add(-time.Minute).UTC()).Error; err != nil {
					t.Fatal(err)
				}
				if err := executor.Execute(t.Context(), id); err != nil || provider.creates != 1 {
					t.Fatalf("settled retry err=%v provider=%+v", err, provider)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var paid struct{ RefundedCents int64 }
			if err := tx.Table("payment_order").Where("id=?", refundPayOrdID).Take(&paid).Error; err != nil {
				t.Fatal(err)
			}
			if !scenario.missing {
				if provider.creates != 0 || paid.RefundedCents != 100 {
					t.Fatalf("existing success discarded: %+v %+v", paid, provider)
				}
			} else if paid.RefundedCents != 0 || provider.creates != 1 {
				t.Fatalf("unconfirmed money moved: %+v %+v", paid, provider)
			}
		})
	}
}

// TestHistoricalManualReviewRequiresSettlement 验证首签、二签的费用闸门及无回执时仍可驳回。
func TestHistoricalManualReviewRequiresSettlement(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "approve", true: "reject"}[reject], func(t *testing.T) {
			db, closeDB := testConn(t)
			defer closeDB()
			tx := db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			seedRefundableOrder(t, tx)
			repo := RefundReviewRepo{LookupSigner: func(context.Context, uint64) (SignerStatus, error) {
				return SignerStatus{Role: "customer_finance", Permitted: true}, nil
			}}
			_, no, id, err := repo.ManualCreate(t.Context(), tx, ManualRefundInput{ActorID: refundActorID, OrderID: refundOrderID, RequestID: "review-guard", AmountCents: 100, Reason: "历史复核"})
			if err != nil {
				t.Fatal(err)
			}
			remove := func() {
				t.Helper()
				if err := tx.Table("charge_fee_receipt").Where("charge_order_id=?", refundOrderID).Delete(nil).Error; err != nil {
					t.Fatal(err)
				}
			}
			restore := func() {
				t.Helper()
				if err := tx.Table("charge_fee_receipt").Create(map[string]any{"charge_order_id": refundOrderID, "calculation_no": "CALC-REVIEW", "result_json": `{"total_cents":10000}`}).Error; err != nil {
					t.Fatal(err)
				}
			}
			remove()
			if reject {
				outcome, _, err := repo.Review(t.Context(), tx, no, ReviewDecision{Approve: false, ActorID: refundActorID, Reason: "撤销"})
				if err != nil || outcome != RefundOutcomeRejected {
					t.Fatalf("reject outcome=%s err=%v", outcome, err)
				}
				return
			}
			for _, actor := range []uint64{refundActorID, refundActorID + 1} {
				decision := ReviewDecision{Approve: true, ActorID: actor, ApproveComment: "核实"}
				if _, _, err := repo.Review(t.Context(), tx, no, decision); !errors.Is(err, ErrRefundNotSettled) {
					t.Fatalf("actor %d err=%v", actor, err)
				}
				restore()
				outcome, _, err := repo.Review(t.Context(), tx, no, decision)
				want := RefundOutcomeAwaitingSecond
				if actor != refundActorID {
					want = RefundOutcomeApproved
				}
				if err != nil || outcome != want {
					t.Fatalf("actor %d outcome=%s err=%v", actor, outcome, err)
				}
				remove()
			}
			var record RefundRecord
			if err := tx.Where("id=?", id).Take(&record).Error; err != nil || record.ExecutionPolicy != money.ExecutionAutomatic {
				t.Fatalf("record=%+v err=%v", record, err)
			}
		})
	}
}
