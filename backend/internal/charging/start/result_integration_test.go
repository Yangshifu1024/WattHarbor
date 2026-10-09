//go:build integration

package start

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"testing"
	"time"

	"chargepilot/internal/platform/dbconn"
	"chargepilot/internal/testfixture"
	"gorm.io/gorm"
)

// 启动失败回执必须原子退款；重放不得重复增加余额或重复派发事件。
func TestBalanceStartRejectionRefundsOnce(t *testing.T) {
	db, closeDB := testConn(t)
	defer closeDB()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := testfixture.NewID()
	orderNo := "C-START-" + rand.Text()
	month := time.Now().UTC().Format("2006-01") + "-01"
	for _, seed := range []struct {
		table string
		row   map[string]any
	}{
		{"wallet_account", map[string]any{"id": id, "user_id": id, "balance_cents": 100, "status": "active"}},
		{"payment_order", map[string]any{"id": id, "order_no": "P-" + orderNo, "biz_type": "charge", "biz_id": id, "user_id": id, "pay_method": "balance", "total_cents": 1000, "paid_cents": 1000, "status": "paid", "created_month": month}},
		{"charge_order", map[string]any{"id": id, "order_no": orderNo, "user_id": id, "payment_order_id": id, "device_id": "START-REFUND-TEST", "port_no": 1, "status": "paid", "payment_status": "paid", "total_cents": 1000, "electric_cents": 1000, "created_month": month}},
	} {
		if err := tx.Table(seed.table).Create(seed.row).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := StartResultService{DB: tx}
	result := StartResult{CommandID: "33333333-3333-4333-8333-333333333333", ChargeOrderID: id, OrderNo: orderNo, DeviceID: "START-REFUND-TEST", PortNo: 1, PortID: id, ResultCode: 1, OccurredAt: time.Now().UTC().Truncate(time.Millisecond)}
	if replayed, err := service.Apply(context.Background(), result); err != nil || replayed {
		t.Fatalf("first: replayed=%v err=%v", replayed, err)
	}
	if replayed, err := service.Apply(context.Background(), result); err != nil || !replayed {
		t.Fatalf("replay: replayed=%v err=%v", replayed, err)
	}
	result.ResultCode = 2
	if _, err := service.Apply(context.Background(), result); !errors.Is(err, ErrStartResultConflict) {
		t.Fatalf("changed receipt: %v", err)
	}
	var balance int64
	if err := tx.Table("wallet_account").Where("id=?", id).Select("balance_cents").Scan(&balance).Error; err != nil || balance != 1100 {
		t.Fatalf("balance=%d err=%v", balance, err)
	}
	var payment struct {
		// RefundedCents 从 payment_order 读取的已退款金额，单位为分，用于核对启动失败只退款一次。
		RefundedCents int64
		// Status 支付单当前持久支付、退款状态，用于核对启动拒绝退款完成。
		Status string
	}
	if err := tx.Table("payment_order").Where("id=?", id).Take(&payment).Error; err != nil || payment.RefundedCents != 1000 || payment.Status != "refunded" {
		t.Fatalf("payment=%+v err=%v", payment, err)
	}
	var order struct {
		// Status,PaymentStatus 分别为订单和支付状态；启动拒绝并退回余额后两者须为 refunded。
		Status, PaymentStatus string
	}
	if err := tx.Table("charge_order").Where("id=?", id).Take(&order).Error; err != nil || order.Status != "refunded" || order.PaymentStatus != "refunded" {
		t.Fatalf("order=%+v err=%v", order, err)
	}
	for _, check := range []struct {
		table, where string
		arg          any
	}{
		{"wallet_txn", "user_id=?", id}, {"refund_record", "biz_id=?", id},
		{"charge_start_receipt", "charge_order_id=?", id}, {"charge_event_log", "charge_order_id=?", id},
		{"event_outbox", "event_id=?", result.CommandID},
	} {
		var count int64
		if err := tx.Table(check.table).Where(check.where, check.arg).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", check.table, count, err)
		}
	}
}

const testMySQLURLEnv = "TEST_MYSQL_URL"

// testConn 使用 t 记录初始化错误，从必需的 TEST_MYSQL_URL 以十秒初始化 context 打开真实 SQL 池并包装 GORM。
// 成功返回 ORM 与关闭函数，由调用方在事务及任务结束后调用；缺配置、连接或包装错误由 t.Fatal 终止，不跳过测试。
func testConn(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	raw := os.Getenv(testMySQLURLEnv)
	if raw == "" {
		t.Fatalf("%s 未设置。真库集成测试不提供跳过路径：没有库就是没测，不是测过了。", testMySQLURLEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sqlDB, err := dbconn.Open(ctx, raw)
	if err != nil {
		t.Fatalf("连接测试数据库失败：%v", err)
	}
	orm, err := dbconn.WrapGORM(sqlDB)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatalf("包装 GORM 失败：%v", err)
	}
	return orm, func() { _ = sqlDB.Close() }
}
