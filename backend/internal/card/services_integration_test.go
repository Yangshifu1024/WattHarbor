//go:build integration

package card

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"wattharbor/internal/domain/charging"
	"wattharbor/internal/domain/money"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/order"
	"wattharbor/internal/platform/dbconn"
	"wattharbor/internal/protocol"
	"wattharbor/internal/testfixture"
)

// 测试替身Cutoff 当前读 测试替身 复现注入接口；实际 stop 能力在外部包的两连接测试中覆盖。
type fixtureCutoff struct{}

// FrozenInTx 在事务中当前读取 id 截止；返回存在标志。
func (fixtureCutoff) FrozenInTx(tx *gorm.DB, id uint64) (bool, error) {
	var row struct {
		ChargeOrderID uint64 /* 订单 */
	}
	err := tx.Table("charge_billing_cutoff").Clauses(clause.Locking{Strength: "SHARE"}).Where("charge_order_id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

// cardConn 为 t 打开独立真实 MySQL 连接并注册关闭；缺配置或失败终止，不跳过。
func cardConn(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_URL")
	if raw == "" {
		t.Fatal("TEST_MYSQL_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := dbconn.Open(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}

// seedCardUser 在 db 中写入 id 用户及 cents 钱包；测试负责回滚或身份清理，不覆盖现存用户。
func seedCardUser(t *testing.T, db *gorm.DB, id uint64, cents int64) {
	t.Helper()
	if err := db.Table("user").Create(map[string]any{"id": id, "openid": uuid.NewString(), "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("wallet_account").Create(map[string]any{"user_id": id, "balance_cents": cents, "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
}

// swipeFacts 是设备发现、能力和当前方案 测试替身；仅数据库资金与状态是本测试真实实现。
type swipeFacts struct {
	port  money.ScanResult // 设备端口信息
	rule  charging.Rule    // 当前报价，续购测试故意替换此值
	reads int              // 实际报价读取次数
}

// ResolveScan 返回 ctx/code 设备查询 测试替身；不发送网络请求或写库。
func (f *swipeFacts) ResolveScan(ctx context.Context, code string) (money.ScanResult, error) {
	return f.port, nil
}

// Capabilities 返回 ctx/station/device 的 DC589 能力 测试替身。
func (f *swipeFacts) Capabilities(ctx context.Context, station uint64, device string) (protocol.AdapterID, charging.Capabilities, error) {
	cap, err := charging.ProtocolCapabilities(protocol.AdapterIDDC589)
	return protocol.AdapterIDDC589, cap, err
}

// ActiveDeviceRule 返回 ctx/station/device 当前报价 测试替身，记录读取次数。
func (f *swipeFacts) ActiveDeviceRule(ctx context.Context, station uint64, device string) (charging.Rule, error) {
	f.reads++
	return f.rule, nil
}

// CheckDeviceScheme 按 ctx/station/device 的 DC589 测试替身 核对 scheme。
func (f *swipeFacts) CheckDeviceScheme(ctx context.Context, station uint64, device string, scheme charging.Scheme) error {
	cap, _ := charging.ProtocolCapabilities("dc589")
	return scheme.ValidateCapabilities(cap)
}

// TestSwipeOwnsReplayFrozenExtensionCutoffAndFunds 使用真实卡、订单、支付和钱包表，外层回滚。
// 设备信息为 测试替身；不发送实际命令，不证明实机起充。相同事件恢复不重新检查时效、设备。
func TestSwipeOwnsReplayFrozenExtensionCutoffAndFunds(t *testing.T) {
	db := cardConn(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	user := testfixture.NewID()
	seedCardUser(t, tx, user, 5000)
	cardNo := strconv.FormatUint(user%4000000000+1, 10)
	card := OnlineCard{CardNo: cardNo, UserID: user, Status: "active"}
	if err := tx.Create(&card).Error; err != nil {
		t.Fatal(err)
	}
	portCode := "CARD" + uuid.NewString()
	deviceID := "D" + strconv.FormatUint(user, 10)
	scheme := charging.Scheme{Name: "frozen card proof", Packages: []charging.Package{{ID: 1, Name: "half hour", Mode: "duration", PriceCents: 300, Minutes: 30}}, Card: charging.CardPolicy{PackageID: 1, MaxMinutes: 180}}
	facts := &swipeFacts{port: money.ScanResult{Kind: "port", DeviceID: deviceID, StationID: user, DeviceStatus: "enabled", Port: &money.ScanPort{PortID: portCode, DeviceID: deviceID, PortNo: 1, Online: true, Available: true}}, rule: charging.Rule{ID: user, Version: 1, StationID: user, DeviceID: deviceID, Spec: charging.Spec{Scheme: &scheme}}}
	service := SwipeService{DB: tx, Ports: facts, Devices: facts, Pricing: facts, Cutoff: fixtureCutoff{}}
	input := SwipeInput{CardNo: cardNo, EventID: uuid.NewString(), PortID: portCode, OccurredAt: time.Now()}
	first, err := service.Apply(context.Background(), input)
	if err != nil || first.Kind != "start" || first.PriceCents != 300 || first.Minutes != 30 {
		t.Fatalf("first=%+v %v", first, err)
	}
	input.OccurredAt = time.Now().Add(-time.Hour)
	facts.port.Port.Online = false
	replay, err := service.Apply(context.Background(), input)
	if err != nil || replay.OperationID != first.OperationID {
		t.Fatalf("expired/offline replay=%+v %v", replay, err)
	}
	input.EventID = uuid.NewString()
	if _, err := service.Apply(context.Background(), input); err == nil {
		t.Fatal("new expired event accepted")
	}
	input.OccurredAt = time.Now()
	facts.port.Port.Online = true
	if _, err := ConfirmStartOperation(tx, first.ChargeOrderID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Model(&order.ChargeOrderRecord{}).Where("id=?", first.ChargeOrderID).Updates(map[string]any{"status": "charging", "started_at": time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	scheme.Packages[0].PriceCents = 2000
	facts.rule.Spec.Scheme = &scheme
	extended, err := service.Apply(context.Background(), input)
	if err != nil || extended.Kind != "extend" || extended.PriceCents != 300 || facts.reads != 1 {
		t.Fatalf("frozen extension=%+v reads=%d %v", extended, facts.reads, err)
	}
	var session CardCharge
	if err := tx.Where("charge_order_id=?", first.ChargeOrderID).Take(&session).Error; err != nil {
		t.Fatal(err)
	}
	if session.PurchasedMinutes != 60 || session.PaidCents != 600 {
		t.Fatalf("session=%+v", session)
	}
	var payment struct {
		// PaidCents 从 payment_order 读取的实付累计金额，单位为分，与刷卡加时后的购买金额比对。
		PaidCents int64
	}
	if err := tx.Table("payment_order").Where("biz_id=?", first.ChargeOrderID).Take(&payment).Error; err != nil || payment.PaidCents != 600 {
		t.Fatalf("payment=%+v %v", payment, err)
	}
	var balance struct {
		// BalanceCents 从 wallet_account 读取的余额，单位为分，用于确认未重复扣款或正确累计扣款。
		BalanceCents int64
	}
	if err := tx.Table("wallet_account").Where("user_id=?", user).Take(&balance).Error; err != nil || balance.BalanceCents != 4400 {
		t.Fatalf("balance=%+v %v", balance, err)
	}
	if err := tx.Table("charge_billing_cutoff").Create(map[string]any{"charge_order_id": first.ChargeOrderID, "cutoff_at": time.Now(), "reason": "card_deadline"}).Error; err != nil {
		t.Fatal(err)
	}
	input.EventID = uuid.NewString()
	if _, err := service.Apply(context.Background(), input); !errors.Is(err, ErrCardOperation) {
		t.Fatalf("cutoff extension=%v", err)
	}
	var count int64
	if err := tx.Table("card_operation").Where("charge_order_id=?", first.ChargeOrderID).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("operations=%d %v", count, err)
	}
}

// TestCardManagementPreservesOwnershipReplayAndAtomicAudit 校验真实用户归属及人工、用户审计，外层回滚。
func TestCardManagementPreservesOwnershipReplayAndAtomicAudit(t *testing.T) {
	db := cardConn(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	user := testfixture.NewID()
	seedCardUser(t, tx, user, 1000)
	seedCardUser(t, tx, user+1, 1000)
	actor := operations.Profile{ID: user, Permissions: []string{"online_card.manage", "user.read"}}
	admin := CardAdminService{DB: tx}
	status := CardStatusService{DB: tx}
	in := CardBindInput{CardNo: strconv.FormatUint(user%4000000000+1, 10), UserID: user, Reason: "verified ownership"}
	id, err := admin.Bind(context.Background(), actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := admin.Bind(context.Background(), actor, in); err != nil || again != id {
		t.Fatalf("bind replay=%d %v", again, err)
	}
	in.UserID = user + 1
	if _, err := admin.Bind(context.Background(), actor, in); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("other owner=%v", err)
	}
	if err := status.Change(context.Background(), user+1, in.CardNo, "lost"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("other user status=%v", err)
	}
	if err := status.Change(context.Background(), user, in.CardNo, "lost"); err != nil {
		t.Fatal(err)
	}
	if balance, err := status.Balance(context.Background(), in.CardNo); err != nil || balance.Valid || balance.BalanceCents != 0 {
		t.Fatalf("lost balance=%+v %v", balance, err)
	}
	if err := admin.ChangeStatus(context.Background(), actor, id, CardChangeInput{Status: "active", Reason: "found"}); err != nil {
		t.Fatal(err)
	}
	fault := errors.New("injected card audit failure")
	const callback = "card_atomic_audit_proof"
	if err := tx.Callback().Create().Before("gorm:create").Register(callback, func(db *gorm.DB) {
		if db.Statement.Table == "online_card_audit" {
			db.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := status.Change(context.Background(), user, in.CardNo, "unbound"); !errors.Is(err, fault) {
		t.Fatalf("audit failure=%v", err)
	}
	tx.Callback().Create().Remove(callback)
	var card OnlineCard
	if err := tx.Where("id=?", id).Take(&card).Error; err != nil || card.Status != "active" {
		t.Fatalf("audit rollback=%+v %v", card, err)
	}
	if err := status.Change(context.Background(), user, in.CardNo, "unbound"); err != nil {
		t.Fatal(err)
	}
	if err := admin.ChangeStatus(context.Background(), actor, id, CardChangeInput{Status: "active", Reason: "invalid reactivation"}); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("unbound reactivation=%v", err)
	}
	in.UserID = user + 1
	if rebound, err := admin.Bind(context.Background(), actor, in); err != nil || rebound != id {
		t.Fatalf("rebind=%d %v", rebound, err)
	}
	var count int64
	if err := tx.Table("online_card_audit").Where("card_id=?", id).Count(&count).Error; err != nil || count != 5 {
		t.Fatalf("audit count=%d %v", count, err)
	}
	rows, err := status.List(context.Background(), user)
	if err != nil || len(rows) != 0 {
		t.Fatalf("old owner list=%+v %v", rows, err)
	}
}
