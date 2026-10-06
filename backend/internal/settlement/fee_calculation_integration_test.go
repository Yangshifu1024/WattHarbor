//go:build integration

package settlement

import (
	"context"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"gorm.io/gorm"
)

// 计费的真库并发与幂等测试。
//
// Calculate 的结构是「按 charge_order_id upsert 回执 → 锁回执行 → 看
// calculation_id 是否已填」。填了就走重放路径返回首次结果，没填就新建计价。
// 这套判重的正确性只在真库的 upsert、行锁与唯一键下才存在，
// 单测（只覆盖 PriceSource）验不到。

// feeSource 造一份可计价来源。走 FinalFee 那条路而非实际计量：
// 后者需要一份完整的 charging.Spec.Scheme，凭空构造的 Spec 会被正确地拒绝。
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

// cleanupFee 接收测试句柄 t、真库句柄 db、充电主键 chargeOrderID 与订单号 orderNo，按两种身份删除计价投递、回执及计算记录。
// 无返回值，逐项 SQL 失败由 t.Errorf 记录并继续；不打开事务或关闭池，调用方负责本测试身份与清理时机。
func cleanupFee(t *testing.T, db *gorm.DB, chargeOrderID uint64, orderNo string) {
	t.Helper()
	for _, q := range []struct {
		// table,where table 为本测试清理或计数的目标表；where 为限定本测试记录身份的参数化条件，保持两者原顺序。
		table, where string
		// args 原条件占位符对应的参数切片，展开顺序与 where 一致，不拼接参数到 SQL。
		args []any
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

// TestConcurrentCalculateProducesSingleResult 钉住并发计费只出一份结果。
//
// 同一笔充电订单被两个计费任务同时处理（worker 重跑、补跑、并发分片），
// 只能落一份计价、只投递一次。断言：计价表一行、投递表一行、
// 两笔调用拿到同一个计算单号且都不报错。
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
	// outcome 将真实计价结果与错误传回并发测试控制方，不替计算器创建事务。
	type outcome struct {
		// res 当前真实计价返回的结果，包含计算单号与分为单位的金额，供并发唯一结果核对。
		res Result
		// err 当前调用返回的执行错误；nil 表示该调用成功，不替代持久状态核验。
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

// TestCalculateRejectsDifferentSourceForSameOrder 钉住同单不同来源被判冲突。
//
// 同一笔充电订单若换了一份来源（改了单号或金额），绝不能沿用首次结果，
// 也不能再出一条计价——必须判冲突，让调用方去查为什么。
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
