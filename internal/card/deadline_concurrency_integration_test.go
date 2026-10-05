//go:build integration

package card_test

import (
	"context"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/card"
	"github.com/ChargePilot2026/charge-pilot/internal/charging/stop"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestExtensionAndDeadlineSerializePurchasedDuration 控制两条真库连接在会话锁上等待，核对两种提交次序。
// 续购先提交则到期按新时长判断；截止先提交则续购不得再扣款，两者均保留支付和钱包一致性。
func TestExtensionAndDeadlineSerializePurchasedDuration(t *testing.T) {
	for _, extensionFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "extension-first", false: "deadline-first"}[extensionFirst], func(t *testing.T) {
			first, second := cutoffCardConn(t), cutoffCardConn(t)
			id, cardNo, port, deviceID := seedCutoffCard(t, first)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			facts := cutoffCardFacts{port: money.ScanResult{Kind: "port", DeviceID: deviceID, StationID: id, DeviceStatus: "enabled", Port: &money.ScanPort{PortID: port, DeviceID: deviceID, PortNo: 1, Online: true}}}
			service := func(db *gorm.DB) card.SwipeService {
				return card.SwipeService{DB: db, Ports: facts, Devices: facts, Pricing: facts, Cutoff: stop.CutoffRepo{}}
			}
			locked, attempt, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var lockOnce, attemptOnce sync.Once
			const hold = "card_session_race_hold"
			const wait = "card_session_race_wait"
			if err := first.Callback().Query().After("gorm:query").Register(hold, func(tx *gorm.DB) {
				if tx.Statement.Table == "card_charge" && strings.Contains(tx.Statement.SQL.String(), "FOR UPDATE") && tx.Error == nil {
					lockOnce.Do(func() {
						close(locked)
						select {
						case <-release:
						case <-ctx.Done():
							tx.AddError(ctx.Err())
						}
					})
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer first.Callback().Query().Remove(hold)
			if err := second.Callback().Query().Before("gorm:query").Register(wait, func(tx *gorm.DB) {
				if tx.Statement.Table == "card_charge" {
					attemptOnce.Do(func() { close(attempt) })
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer second.Callback().Query().Remove(wait)
			type result struct {
				frozen bool  /* 是否到期冻结 */
				err    error /* 原用例结果 */
			}
			one, two := make(chan result, 1), make(chan result, 1)
			runExtension := func(db *gorm.DB, done chan result) {
				_, err := service(db).Apply(ctx, card.SwipeInput{CardNo: cardNo, EventID: uuid.NewString(), PortID: port, OccurredAt: time.Now()})
				done <- result{err: err}
			}
			runDeadline := func(db *gorm.DB, done chan result) {
				frozen, err := stop.FreezeDeadline(ctx, db, id, time.Now().Add(31*time.Minute))
				done <- result{frozen, err}
			}
			if extensionFirst {
				go runExtension(first, one)
			} else {
				go runDeadline(first, one)
			}
			select {
			case <-locked:
			case <-ctx.Done():
				t.Fatal("first session lock not reached")
			}
			if extensionFirst {
				go runDeadline(second, two)
			} else {
				go runExtension(second, two)
			}
			select {
			case <-attempt:
			case <-ctx.Done():
				t.Fatal("second session lock not attempted")
			}
			close(release)
			var a, b result
			select {
			case a = <-one:
			case <-ctx.Done():
				t.Fatal("first request did not finish")
			}
			select {
			case b = <-two:
			case <-ctx.Done():
				t.Fatal("second request did not finish")
			}
			if extensionFirst {
				if a.err != nil || b.err != nil || b.frozen {
					t.Fatalf("extension then deadline=%+v/%+v", a, b)
				}
			} else {
				if a.err != nil || !a.frozen || !errors.Is(b.err, card.ErrCardOperation) {
					t.Fatalf("deadline then extension=%+v/%+v", a, b)
				}
			}
			minutes, paid, balance, operations, cutoffs := 30, int64(100), int64(1000), int64(0), int64(1)
			if extensionFirst {
				minutes, paid, balance, operations, cutoffs = 60, 200, 900, 1, 0
			}
			var session card.CardCharge
			if err := first.Where("charge_order_id=?", id).Take(&session).Error; err != nil || session.PurchasedMinutes != uint16(minutes) || session.PaidCents != paid {
				t.Fatalf("session=%+v %v", session, err)
			}
			var funding struct {
				PaidCents  int64 /* 已付分 */
				TotalCents int64 /* 应付分 */
			}
			if err := first.Table("payment_order").Where("id=?", id).Take(&funding).Error; err != nil || funding.PaidCents != paid || funding.TotalCents != paid {
				t.Fatalf("funding=%+v %v", funding, err)
			}
			var wallet struct {
				BalanceCents int64 /* 钱包剩余分 */
			}
			if err := first.Table("wallet_account").Where("user_id=?", id).Take(&wallet).Error; err != nil || wallet.BalanceCents != balance {
				t.Fatalf("wallet=%+v %v", wallet, err)
			}
			for _, row := range []struct {
				table    string /* 所属凭据表 */
				expected int64  /* 应有条数 */
			}{{"card_operation", operations}, {"charge_billing_cutoff", cutoffs}} {
				var n int64
				if err := first.Table(row.table).Where("charge_order_id=?", id).Count(&n).Error; err != nil || n != row.expected {
					t.Fatalf("%s=%d %v", row.table, n, err)
				}
			}
		})
	}
}

// TestConcurrentSameCardEventDebitsOnce 控制第二次原事件请求在卡锁上等待第一事务，核对只生成一次续购凭据。
// 两条连接均调用真实 SwipeService；设备及价格只为 fixture，钱包、订单、支付和会话使用真实表。
func TestConcurrentSameCardEventDebitsOnce(t *testing.T) {
	first, second := cutoffCardConn(t), cutoffCardConn(t)
	id, cardNo, port, deviceID := seedCutoffCard(t, first)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	facts := cutoffCardFacts{port: money.ScanResult{Kind: "port", DeviceID: deviceID, StationID: id, DeviceStatus: "enabled", Port: &money.ScanPort{PortID: port, DeviceID: deviceID, PortNo: 1, Online: true}}}
	in := card.SwipeInput{CardNo: cardNo, EventID: uuid.NewString(), PortID: port, OccurredAt: time.Now()}
	locked, attempt, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var lockOnce, attemptOnce sync.Once
	const hold = "same_card_event_hold"
	const wait = "same_card_event_wait"
	if err := first.Callback().Query().After("gorm:query").Register(hold, func(tx *gorm.DB) {
		if tx.Statement.Table == "online_card" && strings.Contains(tx.Statement.SQL.String(), "FOR UPDATE") && tx.Error == nil {
			lockOnce.Do(func() {
				close(locked)
				select {
				case <-release:
				case <-ctx.Done():
					tx.AddError(ctx.Err())
				}
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer first.Callback().Query().Remove(hold)
	if err := second.Callback().Query().Before("gorm:query").Register(wait, func(tx *gorm.DB) {
		if tx.Statement.Table == "online_card" {
			attemptOnce.Do(func() { close(attempt) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer second.Callback().Query().Remove(wait)
	type result struct {
		operation card.CardOperation /* 原操作 */
		err       error              /* 用例错误 */
	}
	one, two := make(chan result, 1), make(chan result, 1)
	run := func(db *gorm.DB, done chan result) {
		operation, err := (card.SwipeService{DB: db, Ports: facts, Devices: facts, Pricing: facts, Cutoff: stop.CutoffRepo{}}).Apply(ctx, in)
		done <- result{operation, err}
	}
	go run(first, one)
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal("first card lock not reached")
	}
	go run(second, two)
	select {
	case <-attempt:
	case <-ctx.Done():
		t.Fatal("second card lock not attempted")
	}
	close(release)
	var a, b result
	select {
	case a = <-one:
	case <-ctx.Done():
		t.Fatal("first event did not finish")
	}
	select {
	case b = <-two:
	case <-ctx.Done():
		t.Fatal("second event did not finish")
	}
	if a.err != nil || b.err != nil || a.operation.OperationID == "" || a.operation.OperationID != b.operation.OperationID {
		t.Fatalf("original event=%+v/%+v", a, b)
	}
	var session card.CardCharge
	if err := first.Where("charge_order_id=?", id).Take(&session).Error; err != nil || session.PurchasedMinutes != 60 || session.PaidCents != 200 {
		t.Fatalf("session=%+v %v", session, err)
	}
	var funding struct {
		PaidCents int64 /* 原实付分 */
	}
	if err := first.Table("payment_order").Where("id=?", id).Take(&funding).Error; err != nil || funding.PaidCents != 200 {
		t.Fatalf("funding=%+v %v", funding, err)
	}
	var wallet struct {
		BalanceCents int64 /* 钱包剩余分 */
	}
	if err := first.Table("wallet_account").Where("user_id=?", id).Take(&wallet).Error; err != nil || wallet.BalanceCents != 900 {
		t.Fatalf("wallet=%+v %v", wallet, err)
	}
	for _, query := range []struct {
		table, where string /* 所属凭据表及本测试身份 */
		value        any    /* 原测试身份 */
	}{{"card_operation", "charge_order_id=?", id}, {"wallet_txn", "user_id=?", id}} {
		var n int64
		if err := first.Table(query.table).Where(query.where, query.value).Count(&n).Error; err != nil || n != 1 {
			t.Fatalf("%s=%d %v", query.table, n, err)
		}
	}
}
