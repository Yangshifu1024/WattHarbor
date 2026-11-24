//go:build integration

package settlement

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"
	"wattharbor/internal/domain/charging"
)

// 计费真库测试验证 upsert、行锁和唯一键：先登记回执，再锁行，已有 calculation_id 就返回首次结果。

// feeSource 构造人工核定金额的计费来源；实际计量计价需要完整有效的方案。
func feeSource(chargeOrderID uint64, orderNo string) Source {
	fee := charging.Fee{ElectricCents: 300, ServiceCents: 100, TotalCents: 400}
	return Source{
		FinalFee:      &fee,
		ReviewID:      "RV-CONC",
		ChargeOrderID: chargeOrderID,
		OrderNo:       orderNo,
		UserID:        52001,
		Rule:          charging.Rule{ID: 1, StationID: 9001, Version: 1},
		Offer:         &charging.Offer{ID: 1, PriceCents: 1000},
		Meter:         charging.ActualMeter{ChargedWh: 5000, ChargedSeconds: 3600, EndedAt: time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC)},
	}
}

// cleanupFee 按本测试订单 ID 和订单号清理投递、回执及计价记录；逐项报告失败。
func cleanupFee(t *testing.T, db *gorm.DB, chargeOrderID uint64, orderNo string) {
	t.Helper()
	for _, q := range []struct {
		// table、where 指定清理表和本测试身份条件，保持配对。
		table, where string
		args         []any
	}{
		{"fee_delivery", "charge_order_id = ?", []any{chargeOrderID}},
		{"fee_receipt", "charge_order_id = ?", []any{chargeOrderID}},
		{"fee_calculation", "order_no = ?", []any{orderNo}},
	} {
		if err := db.Table(q.table).Where(q.where, q.args...).Delete(nil).Error; err != nil {
			t.Errorf("清理 %s 失败：%v", q.table, err)
		}
	}
}

// TestConcurrentCalculateProducesSingleResult 验证同一订单并发计费只有一份计价和投递，两个调用返回相同计算号。
func TestConcurrentCalculateProducesSingleResult(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	const chargeOrderID = uint64(52001)
	const orderNo = "C-FEE-CONC-1"

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupFee(t, janitor, chargeOrderID, orderNo) })
	cleanupFee(t, setup, chargeOrderID, orderNo)

	p1, close1 := testConn(t)
	defer close1()
	p2, close2 := testConn(t)
	defer close2()

	gate := newLockGate()
	armOnLock(t, p1, gate, "test:fee_lock", "fee_receipt")
	defer gate.pass()

	src := feeSource(chargeOrderID, orderNo)
	// outcome 汇总两个并发计价调用的结果。
	type outcome struct {
		// res 保存计算单号和费用，金额单位为分。
		res Result
		err error
	}
	done1 := make(chan outcome, 1)
	go func() {
		res, err := FeeCalculator{DB: p1}.Calculate(context.Background(), src)
		done1 <- outcome{res, err}
	}()
	gate.await(t, "第一次计费")

	done2 := make(chan outcome, 1)
	go func() {
		res, err := FeeCalculator{DB: p2}.Calculate(context.Background(), src)
		done2 <- outcome{res, err}
	}()

	select {
	case got := <-done2:
		t.Fatalf("第一次计费仍持回执行锁时第二次就返回了（%v）——回执行锁没有串行化并发计费", got.err)
	case <-time.After(2 * time.Second):
	}

	gate.pass()
	first, second := <-done1, <-done2
	if first.err != nil {
		t.Fatalf("第一次计费失败：%v", first.err)
	}
	if second.err != nil {
		t.Fatalf("第二次计费应走重放路径返回首次结果，却报错：%v", second.err)
	}
	if first.res.CalculationNo == "" || first.res.CalculationNo != second.res.CalculationNo {
		t.Errorf("两次算出的计算单号不一致：%q 与 %q", first.res.CalculationNo, second.res.CalculationNo)
	}
	if first.res.TotalCents != 400 || second.res.TotalCents != 400 {
		t.Errorf("重放结果金额应为首次的 400 分，实际 %d 与 %d", first.res.TotalCents, second.res.TotalCents)
	}

	verify, closeVerify := testConn(t)
	defer closeVerify()
	var calcs, deliveries int64
	if err := verify.Table("fee_calculation").Where("order_no = ?", orderNo).Count(&calcs).Error; err != nil {
		t.Fatalf("统计计价记录失败：%v", err)
	}
	if err := verify.Table("fee_delivery").Where("charge_order_id = ?", chargeOrderID).Count(&deliveries).Error; err != nil {
		t.Fatalf("统计投递记录失败：%v", err)
	}
	if calcs != 1 {
		t.Errorf("计价记录 %d 条，期望 1 条——并发计费重复出账", calcs)
	}
	if deliveries != 1 {
		t.Errorf("投递记录 %d 条，期望 1 条", deliveries)
	}
}

// TestCalculateRejectsDifferentSourceForSameOrder 验证同一订单更改单号或金额时必须报冲突，不复用或新增计价。
func TestCalculateRejectsDifferentSourceForSameOrder(t *testing.T) {
	setup, closeSetup := testConn(t)
	defer closeSetup()
	const chargeOrderID = uint64(52002)
	const orderNo = "C-FEE-CONFLICT-1"

	janitor, closeJanitor := testConn(t)
	t.Cleanup(closeJanitor)
	t.Cleanup(func() { cleanupFee(t, janitor, chargeOrderID, orderNo) })
	cleanupFee(t, setup, chargeOrderID, orderNo)

	store := FeeCalculator{DB: setup}
	first := feeSource(chargeOrderID, orderNo)
	if _, err := store.Calculate(context.Background(), first); err != nil {
		t.Fatalf("首次计费失败：%v", err)
	}

	// 同一笔订单，换一个单号——内容与首次不一致。
	second := feeSource(chargeOrderID, "C-FEE-CONFLICT-1-B")
	if _, err := store.Calculate(context.Background(), second); err == nil {
		t.Error("同单不同来源却接受了，会让首次的计价凭据失去意义")
	}

	var calcs int64
	if err := setup.Table("fee_calculation").Where("order_no IN ?", []string{orderNo, "C-FEE-CONFLICT-1-B"}).
		Count(&calcs).Error; err != nil {
		t.Fatalf("统计计价记录失败：%v", err)
	}
	if calcs != 1 {
		t.Errorf("计价记录 %d 条，期望仍为首次的 1 条", calcs)
	}
}
