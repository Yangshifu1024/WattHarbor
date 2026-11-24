//go:build integration

package settlement

import (
	"context"
	"crypto/rand"
	"database/sql"
	"testing"
	"time"

	"gorm.io/gorm/clause"
	"wattharbor/internal/payment"
	"wattharbor/internal/testfixture"
	"wattharbor/internal/wallet"
)

// TestWalletRefundSourcesDoNotLockPendingRecharge 固定到账持有待支付行、退款持有钱包的锁序。
// 两条独立连接及真实 RR 事务，不依赖 goroutine 调度；退款来源点锁必须在到账支付锁未释放时完成。
func TestWalletRefundSourcesDoNotLockPendingRecharge(t *testing.T) {
	reader, closeReader := testConn(t)
	defer closeReader()
	writer, closeWriter := testConn(t)
	defer closeWriter()
	id := testfixture.NewID()
	defer func() {
		for _, q := range []struct {
			table string // 本测试已提交的来源及钱包表
			where string // 仅筛选本测试身份
		}{{"payment_order", "user_id=?"}, {"wallet_account", "user_id=?"}} {
			if err := writer.Table(q.table).Where(q.where, id).Delete(nil).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := writer.Table(q.table).Where(q.where, id).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("%s remaining=%d %v", q.table, n, err)
			}
		}
	}()
	if err := writer.Table("wallet_account").Create(map[string]any{"id": id, "user_id": id, "balance_cents": 5000, "frozen_cents": 0, "status": "active", "version": 1}).Error; err != nil {
		t.Fatal(err)
	}
	month := time.Now().UTC().Format("2006-01") + "-01"
	for i, status := range []string{"paid", "initiated", "paid"} {
		paid := int64(0)
		var transaction any
		if status == "paid" {
			paid = 5000
			transaction = "TX-" + rand.Text()
		}
		if err := writer.Table("payment_order").Create(map[string]any{"id": id + uint64(i), "order_no": "SOURCE-" + rand.Text(), "user_id": id, "biz_type": "wallet_recharge", "biz_id": 0, "pay_method": "wechat", "total_cents": 5000, "paid_cents": paid, "status": status, "wechat_transaction_id": transaction, "created_month": month}).Error; err != nil {
			t.Fatal(err)
		}
	}
	callback := writer.Begin(&sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if callback.Error != nil {
		t.Fatal(callback.Error)
	}
	defer callback.Rollback()
	var pending payment.PaymentOrderRecord
	if err := callback.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id+1).Take(&pending).Error; err != nil {
		t.Fatal(err)
	}
	refund := reader.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if refund.Error != nil {
		t.Fatal(refund.Error)
	}
	defer refund.Rollback()
	if _, err := (wallet.UserWalletRepo{}).LockWalletAccount(refund, id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sources, err := (payment.RechargeSources{}).LockRechargeSources(refund.WithContext(ctx), id)
	if err != nil {
		t.Fatalf("退款持有钱包时等待到账持有的待付行，存在循环锁序：%v", err)
	}
	if len(sources) != 2 || sources[0].ID != id || sources[1].ID != id+2 || sources[0].PaidCents != 5000 {
		t.Fatalf("来源不正确：%+v", sources)
	}
}
