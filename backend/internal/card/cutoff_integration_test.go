//go:build integration

package card_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"wattharbor/internal/card"
	"wattharbor/internal/charging/stop"
	"wattharbor/internal/domain/charging"
	"wattharbor/internal/domain/money"
	"wattharbor/internal/platform/dbconn"
	"wattharbor/internal/protocol"
	"wattharbor/internal/testfixture"
)

// cutoffCardConn 为 t 打开独立真实连接并注册关闭，缺配置或失败终止，不跳过。
func cutoffCardConn(t *testing.T) *gorm.DB {
	t.Helper()
	url := os.Getenv("TEST_MYSQL_URL")
	if url == "" {
		t.Fatal("TEST_MYSQL_URL is required")
	}
	db, err := dbconn.Open(context.Background(), url)
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

// cutoffCardFacts 仅模拟已在线端口及 DC589 能力；订单、截止和资金均为真实库。
type cutoffCardFacts struct {
	port money.ScanResult /* 端口信息 */
}

// ResolveScan 返回 ctx/code 对应端口 测试替身，无网络或数据库写入。
func (f cutoffCardFacts) ResolveScan(ctx context.Context, code string) (money.ScanResult, error) {
	return f.port, nil
}

// Capabilities 返回 ctx/station/device 的 DC589 能力 测试替身。
func (f cutoffCardFacts) Capabilities(ctx context.Context, station uint64, device string) (protocol.AdapterID, charging.Capabilities, error) {
	cap, err := charging.ProtocolCapabilities(protocol.AdapterIDDC589)
	return protocol.AdapterIDDC589, cap, err
}

// ActiveDeviceRule 拒绝续购读取新报价；入参 ctx/station/device 仅标识目标。
func (f cutoffCardFacts) ActiveDeviceRule(ctx context.Context, station uint64, device string) (charging.Rule, error) {
	return charging.Rule{}, errors.New("extension must not read current pricing")
}

// CheckDeviceScheme 拒绝续购校验新方案；ctx/station/device/scheme 无 I/O。
func (f cutoffCardFacts) CheckDeviceScheme(ctx context.Context, station uint64, device string, scheme charging.Scheme) error {
	return errors.New("extension must not check current pricing")
}

// TestSwipeReadsCutoffCommittedAfterRRSnapshot 用两条真实连接固定旧快照→截止提交→续购的顺序。
// 不依赖概率竞态，截止写入复用 stop 对应模块的接口；拒绝后钱包、时长、支付金额及操作凭据均不变。
func TestSwipeReadsCutoffCommittedAfterRRSnapshot(t *testing.T) {
	db := cutoffCardConn(t)
	other := cutoffCardConn(t)
	id, cardNo, port, no := seedCutoffCard(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx := db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var before int64
	if err := tx.Table("charge_billing_cutoff").Where("charge_order_id=?", id).Count(&before).Error; err != nil || before != 0 {
		t.Fatalf("old cutoff snapshot=%d %v", before, err)
	}
	if err := stop.RecordBillingCutoff(ctx, other, id, "card_deadline", time.Now()); err != nil {
		t.Fatal(err)
	}
	// 首次操作随后提交，同一 RR 快照也不能隐藏这一凭据；重放优先于当前截止检查。
	originalEvent, originalOperation := uuid.NewString(), uuid.NewString()
	if err := other.Table("card_operation").Create(map[string]any{"operation_id": originalOperation, "event_id": originalEvent, "device_id": no, "charge_order_id": id, "card_id": id, "kind": "extend", "price_cents": 100, "minutes": 30, "status": "confirmed"}).Error; err != nil {
		t.Fatal(err)
	}
	facts := cutoffCardFacts{port: money.ScanResult{Kind: "port", DeviceID: no, StationID: id, DeviceStatus: "enabled", Port: &money.ScanPort{PortID: port, DeviceID: no, PortNo: 1, Online: true}}}
	service := card.SwipeService{DB: tx, Ports: facts, Devices: facts, Pricing: facts, Cutoff: stop.CutoffRepo{}}
	if _, err := service.Apply(ctx, card.SwipeInput{CardNo: cardNo, EventID: uuid.NewString(), PortID: port, OccurredAt: time.Now()}); !errors.Is(err, card.ErrCardOperation) {
		t.Fatalf("new cutoff was hidden by RR snapshot: %v", err)
	}
	if original, err := service.Apply(ctx, card.SwipeInput{CardNo: cardNo, EventID: originalEvent, PortID: port, OccurredAt: time.Now()}); err != nil || original.OperationID != originalOperation {
		t.Fatalf("original committed operation hidden by RR snapshot: %+v %v", original, err)
	}
	var session card.CardCharge
	if err := tx.Where("charge_order_id=?", id).Take(&session).Error; err != nil || session.PurchasedMinutes != 30 || session.PaidCents != 100 {
		t.Fatalf("unexpected session debit=%+v %v", session, err)
	}
	var balance struct {
		// BalanceCents 从 wallet_account 读取的余额，单位为分，用于确认未重复扣款或正确累计扣款。
		BalanceCents int64
	}
	if err := tx.Table("wallet_account").Where("user_id=?", id).Take(&balance).Error; err != nil || balance.BalanceCents != 1000 {
		t.Fatalf("unexpected wallet debit=%+v %v", balance, err)
	}
	var count int64
	// 普通读仍是旧快照，应保持零；单独当前读核对只能有首次操作，不能产生新的扣款凭据。
	if err := tx.Table("card_operation").Where("charge_order_id=?", id).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("unexpected operation=%d %v", count, err)
	}
	var current []card.CardOperation
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("charge_order_id=?", id).Find(&current).Error; err != nil || len(current) != 1 || current[0].OperationID != originalOperation {
		t.Fatalf("current operations changed=%+v %v", current, err)
	}
}

// seedCutoffCard 为 t 在 db 提交独立身份的真实续购数据并注册逐表清理；返回订单、卡号、端口和设备编号。
func seedCutoffCard(t *testing.T, db *gorm.DB) (uint64, string, string, string) {
	t.Helper()
	id := testfixture.NewID()
	no := strconv.FormatUint(id, 10)
	cardNo := strconv.FormatUint(id%4000000000+1, 10)
	port := "CUT" + no
	t.Cleanup(func() {
		for _, query := range []struct {
			table, where string
			args         []any
		}{{"card_operation", "charge_order_id=?", []any{id}}, {"wallet_txn", "user_id=?", []any{id}}, {"card_charge", "charge_order_id=?", []any{id}}, {"charge_billing_cutoff", "charge_order_id=?", []any{id}}, {"charge_event_log", "charge_order_id=?", []any{id}}, {"charge_order", "id=?", []any{id}}, {"payment_order", "id=?", []any{id}}, {"online_card", "id=?", []any{id}}, {"wallet_account", "user_id=?", []any{id}}, {"user", "id=?", []any{id}}} {
			if err := db.Table(query.table).Where(query.where, query.args...).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
			var count int64
			if err := db.Table(query.table).Where(query.where, query.args...).Count(&count).Error; err != nil || count != 0 {
				t.Errorf("cleanup %s count=%d %v", query.table, count, err)
			}
		}
	})
	pkg := charging.Package{ID: 1, Name: "frozen", Mode: "duration", PriceCents: 100, Minutes: 30}
	raw, _ := json.Marshal(pkg)
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, entry := range []struct {
			table string
			row   map[string]any
		}{{"user", map[string]any{"id": id, "openid": no, "status": "active"}}, {"wallet_account", map[string]any{"user_id": id, "balance_cents": 1000, "status": "active"}}, {"online_card", map[string]any{"id": id, "user_id": id, "card_no": cardNo, "status": "active"}}, {"payment_order", map[string]any{"id": id, "order_no": "P" + no, "biz_type": "charge", "biz_id": id, "user_id": id, "pay_method": "balance", "total_cents": 100, "paid_cents": 100, "status": "paid", "created_month": time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.UTC)}}, {"charge_order", map[string]any{"id": id, "order_no": "C" + no, "user_id": id, "device_id": no, "port_no": 1, "port_code": port, "payment_order_id": id, "status": "charging", "started_at": time.Now(), "created_month": time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.UTC)}}, {"card_charge", map[string]any{"charge_order_id": id, "card_id": id, "card_no": cardNo, "port_code": port, "active_port": port, "paid_cents": 100, "purchased_minutes": 30, "max_minutes": 180, "package_json": string(raw), "wallet_after_cents": 1000}}} {
			if err := tx.Table(entry.table).Create(entry.row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return id, cardNo, port, no
}
