//go:build integration

package settlement

import (
	"context"
	"crypto/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/testfixture"
	"github.com/ChargePilot2026/charge-pilot/internal/wallet"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestConcurrentRechargeRequestsReplayAfterIdentityRead 验证读取身份后等待用户锁的同请求重放。
// 两条独立连接、指定 user 锁闸门和真实支付唯一键；模拟渠道仅准备参数，真实提交后清理并核对零残留。
func TestConcurrentRechargeRequestsReplayAfterIdentityRead(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	first, closeFirst := testConn(t)
	defer closeFirst()
	second, closeSecond := testConn(t)
	defer closeSecond()
	id := testfixture.NewID()
	request := uuid.NewString()
	if err := setup.Table("user").Create(map[string]any{"id": id, "openid": "same-recharge-" + rand.Text(), "status": "active"}).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		var paymentIDs []uint64
		if err := setup.Table("payment_order").Where("user_id=?", id).Pluck("id", &paymentIDs).Error; err != nil {
			t.Error(err)
		}
		if len(paymentIDs) > 0 {
			if err := setup.Table("charge_prepay").Where("payment_order_id IN ?", paymentIDs).Delete(nil).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := setup.Table("charge_prepay").Where("payment_order_id IN ?", paymentIDs).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("prepay remaining=%d %v", n, err)
			}
		}
		for _, q := range []struct {
			table string // 本测试需要提交的表
			where string // 唯一用户或请求身份，不删除环境原数据
			arg   any    // 对应筛选身份
		}{{"wallet_recharge_request", "request_id=?", request}, {"payment_order", "user_id=?", id}, {"wallet_account", "user_id=?", id}, {"user", "id=?", id}} {
			if err := setup.Table(q.table).Where(q.where, q.arg).Delete(nil).Error; err != nil {
				t.Error(err)
			}
			var n int64
			if err := setup.Table(q.table).Where(q.where, q.arg).Count(&n).Error; err != nil || n != 0 {
				t.Errorf("%s remaining=%d %v", q.table, n, err)
			}
		}
	}()
	gate := newLockGate()
	defer gate.pass()
	key := "recharge_user_" + rand.Text()
	armOnLock(t, first, gate, key, "user")
	defer first.Callback().Query().Remove(key)
	attempting := make(chan struct{})
	var once sync.Once
	if err := second.Callback().Query().Before("gorm:query").Register(key, func(q *gorm.DB) {
		if q.Statement.Table != "user" {
			return
		}
		if _, ok := q.Statement.Clauses["FOR"]; !ok {
			return
		}
		once.Do(func() { close(attempting) })
	}); err != nil {
		t.Fatal(err)
	}
	defer second.Callback().Query().Remove(key)
	// outcome 传递同一充值申请并发 Create 的支付身份、参数与错误，用于核对原支付重放；生成模拟参数不表示支付到账。
	type outcome struct {
		result wallet.RechargeResult // 首次或同内容重放的原充值身份
		err    error                 // 原事务及渠道准备错误
	}
	start := func(db *gorm.DB) <-chan outcome {
		ch := make(chan outcome, 1)
		go func() {
			out, err := (wallet.RechargeService{DB: db, Payments: payment.RechargePaymentRepo{DB: db, Provider: payment.Simulator{}}}).Create(context.Background(), id, wallet.RechargeInput{RequestID: request, AmountCents: 500})
			ch <- outcome{out, err}
		}()
		return ch
	}
	one := start(first)
	gate.await(t, "first recharge user lock")
	two := start(second)
	select {
	case <-attempting:
	case <-time.After(15 * time.Second):
		t.Fatal("second recharge did not reach user lock after identity read")
	}
	gate.pass()
	var results [2]wallet.RechargeResult
	for i, ch := range []<-chan outcome{one, two} {
		select {
		case v := <-ch:
			if v.err != nil {
				t.Fatalf("recharge %d: %v", i, v.err)
			}
			results[i] = v.result
		case <-time.After(15 * time.Second):
			t.Fatal("recharge did not finish")
		}
	}
	if results[0].PaymentOrderID == 0 || results[0].PaymentOrderID != results[1].PaymentOrderID || results[0].MerchantOrderNo != results[1].MerchantOrderNo || string(results[0].PaymentParams) != string(results[1].PaymentParams) || !strings.Contains(string(results[0].PaymentParams), "simulation") {
		t.Fatalf("lost original payment: %+v", results)
	}
	for _, table := range []string{"payment_order", "wallet_recharge_request", "wallet_account"} {
		var n int64
		if err := setup.Table(table).Where("user_id=?", id).Count(&n).Error; err != nil || n != 1 {
			t.Fatalf("%s count=%d %v", table, n, err)
		}
	}
}
