//go:build integration

package settlement

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	charging "github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"github.com/ChargePilot2026/charge-pilot/internal/testfixture"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"github.com/google/uuid"
)

// TestActiveWalletFreezeCountUsesCurrentReadAfterOldSnapshot 防止解冻忽略旧快照之后提交的其他冻结。
// 两连接固定读快照、提交冻结、读取当前聚合的顺序；只验证 repo 机制，造数显式清理为零。
func TestActiveWalletFreezeCountUsesCurrentReadAfterOldSnapshot(t *testing.T) {
	reader, closeReader := testConn(t)
	defer closeReader()
	writer, closeWriter := testConn(t)
	defer closeWriter()
	id := testfixture.NewID()
	defer func() {
		if err := writer.Table("risk_freeze_log").Where("user_id=?", id).Delete(nil).Error; err != nil {
			t.Error(err)
		}
		var n int64
		if err := writer.Table("risk_freeze_log").Where("user_id=?", id).Count(&n).Error; err != nil || n != 0 {
			t.Errorf("remaining freezes=%d %v", n, err)
		}
	}()
	tx := reader.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var n int64
	if err := tx.Table("risk_freeze_log").Where("user_id=?", id).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("snapshot=%d %v", n, err)
	}
	if _, err := (wallet.UserWalletRepo{}).InsertRiskFreeze(writer, id, "wallet_refund_frequency", "wallet", "current-read proof"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("risk_freeze_log").Where("user_id=?", id).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("not an old snapshot=%d %v", n, err)
	}
	if got, err := (wallet.RiskRepo{}).CountActiveFreezes(tx, id); err != nil || got != 1 {
		t.Fatalf("new freeze missed=%d %v", got, err)
	}
}

// TestReviewExistenceUsesCurrentReadAfterOldSnapshot 用两条独立连接证明行锁不会刷新旧 RR 快照。
// 第二连接在第一连接建立快照后提交审核凭据；任务锁内的存在性判断必须看到新凭据，避免重复审计。
// 此测试无需概率并发：快照建立、第二连接提交、第一连接取任务锁顺序确定。
func TestReviewExistenceUsesCurrentReadAfterOldSnapshot(t *testing.T) {
	reader, closeReader := testConn(t)
	defer closeReader()
	writer, closeWriter := testConn(t)
	defer closeWriter()
	id := testfixture.NewID()
	request := uuid.NewString()
	segments := []charging.MeterSegment{{StartedAt: time.Now().UTC().Truncate(time.Millisecond), EnergyWh: 10}}
	segments[0].EndedAt = segments[0].StartedAt.Add(time.Minute)
	corrected, err := json.Marshal(Source{Meter: charging.ActualMeter{Segments: segments}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, table := range []string{"charge_meter_review", "charge_manual_settlement", "charge_billing_job"} {
			if err := writer.Table(table).Where("charge_order_id=?", id).Delete(nil).Error; err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
			var n int64
			if err := writer.Table(table).Where("charge_order_id=?", id).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("remaining %s=%d err=%v", table, n, err)
			}
		}
	}()
	if err := writer.Table("charge_billing_job").Create(map[string]any{"charge_order_id": id, "status": "manual_review"}).Error; err != nil {
		t.Fatal(err)
	}
	tx := reader.WithContext(context.Background()).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var n int64
	if err := tx.Model(&MeterReview{}).Where("request_id=?", request).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("snapshot %d: %v", n, err)
	}
	if err := writer.Table("charge_meter_review").Create(map[string]any{"request_id": request, "charge_order_id": id, "original_json": "{}", "corrected_json": string(corrected), "reason": "proof", "first_reviewer_id": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := writer.Create(&ManualSettlement{ChargeOrderID: id, RequestID: request, ActorID: 1, Reason: "proof"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := LockMeterReviewJob(tx, id); err != nil {
		t.Fatal(err)
	}
	// 普通聚合仍为零，明确证明测试事务确实持有旧快照。
	if err := tx.Model(&MeterReview{}).Where("request_id=?", request).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("old snapshot lost: %d %v", n, err)
	}
	if found, err := MeterProposalExists(tx, request); err != nil || !found {
		t.Fatalf("proposal current read=%v err=%v", found, err)
	}
	if found, err := ManualAmountExists(tx, id, request); err != nil || !found {
		t.Fatalf("manual current read=%v err=%v", found, err)
	}
	if prior, err := (BillingOrders{DB: tx}).ProposeMeter(context.Background(), id, 1, request, "proof", segments); err != nil || prior.RequestID != request {
		t.Fatalf("old snapshot proposal replay: %+v %v", prior, err)
	}
	if err := (BillingOrders{DB: tx}).ResolveAmount(context.Background(), ManualSettlement{ChargeOrderID: id, RequestID: request, ActorID: 1, Reason: "proof"}); err != nil {
		t.Fatalf("old snapshot amount replay: %v", err)
	}
}
