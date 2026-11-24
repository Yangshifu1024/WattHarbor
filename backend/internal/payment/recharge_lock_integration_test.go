//go:build integration

package payment

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"
	"wattharbor/internal/testfixture"
	"wattharbor/internal/wallet"
)

// TestRechargePaymentLookupDoesNotLockOtherRequests 验证另一连接锁住无关申请时，按支付ID定位本次申请仍可完成。
// 实际支付行先锁定，使用真实申请锁操作；只验证锁定位，不执行付款、余额或流水写入。
func TestRechargePaymentLookupDoesNotLockOtherRequests(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	id := testfixture.NewID()
	month := time.Now().UTC().Format("2006-01") + "-01"
	key := uuid.NewString()
	other, target := "A"+key[:35], "Z"+key[:35]
	defer func() {
		for _, row := range []struct {
			table string // 私有夹具所属表。
			where string // 完整主键筛选。
			args  []any  // 本次身份，不按业务范围删除。
		}{
			{"wallet_recharge_request", "request_id IN ?", []any{[]string{other, target}}},
			{"payment_order", "id = ? AND created_month = ?", []any{id, month}},
			{"user", "id = ?", []any{id}},
		} {
			if err := db.Table(row.table).Where(row.where, row.args...).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
				continue
			}
			var n int64
			if err := db.Table(row.table).Where(row.where, row.args...).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("fixture %s remaining=%d err=%v", row.table, n, err)
			}
		}
	}()
	if err := db.Table("user").Create(map[string]any{"id": id, "openid": "recharge-lock-" + key, "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("payment_order").Create(map[string]any{"id": id, "created_month": month, "order_no": "RECHARGE-LOCK-" + key, "biz_type": "wallet_recharge", "biz_id": 0, "user_id": id, "pay_method": "wechat", "total_cents": 500, "paid_cents": 0, "status": "initiated"}).Error; err != nil {
		t.Fatal(err)
	}
	for i, request := range []string{other, target} {
		if err := db.Table("wallet_recharge_request").Create(map[string]any{"request_id": request, "user_id": id, "amount_cents": 500, "payment_order_id": id + 1 - uint64(i)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	locker, closeLocker := testConn(t)
	defer closeLocker()
	blocked := locker.Begin()
	if blocked.Error != nil {
		t.Fatal(blocked.Error)
	}
	defer blocked.Rollback()
	var locked struct {
		RequestID string `gorm:"column:request_id"` /* 另一支付的申请完整主键。 */
	}
	if err := blocked.Table("wallet_recharge_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id=?", other).Take(&locked).Error; err != nil {
		t.Fatal(err)
	}
	reader, closeReader := testConn(t)
	defer closeReader()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx := reader.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var payment PaymentOrderRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND created_month=?", id, month).Take(&payment).Error; err != nil {
		t.Fatal(err)
	}
	receipt, err := wallet.LockRechargeForPayment(tx, id)
	if err != nil || receipt.RequestID != target || receipt.UserID != id || receipt.AmountCents != 500 || receipt.OpenID != "recharge-lock-"+key {
		t.Fatalf("unrelated request lock blocked payment lookup: receipt=%+v err=%v", receipt, err)
	}
}
