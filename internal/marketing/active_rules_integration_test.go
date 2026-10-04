//go:build integration

package marketing

import (
	"fmt"
	"testing"
	"time"
)

// 活动窗口与门槛过滤全部写死在 SQL 的 Where 里，纯函数覆盖不到：
// 边界是开区间还是闭区间、软删除的规则会不会被选中、门槛相等算不算达标，
// 单元测试一个都测不出来，只能对真库验证。这是 marketing 目前唯一的
// 业务空白，补在这里。

// fixedNow 让所有窗口判断与墙上时钟无关。
var fixedNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func hours(n int) time.Time { return fixedNow.Add(time.Duration(n) * time.Hour) }

// TestActiveRulesWindowIsStartInclusiveEndExclusive 钉住活动窗口的两个边界。
//
// start_at <= now AND end_at > now：开始时刻当刻即生效，结束时刻当刻即失效。
// 这两个比较符写反任何一个，活动都会提前或延后一整天生效，
// 而且不会有任何单测失败。
func TestActiveRulesWindowIsStartInclusiveEndExclusive(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedRule(t, tx, 1, TriggerHoliday, hours(-1), hours(1), 0)
	seedRule(t, tx, 2, TriggerHoliday, fixedNow, hours(1), 0)   // 恰好开始
	seedRule(t, tx, 3, TriggerHoliday, hours(-1), fixedNow, 0)  // 恰好结束
	seedRule(t, tx, 4, TriggerHoliday, hours(1), hours(2), 0)   // 尚未开始
	seedRule(t, tx, 5, TriggerHoliday, hours(-2), hours(-1), 0) // 已经结束

	rules, err := activeRules(tx, ActivityEvent{TriggerType: TriggerHoliday, Now: fixedNow})
	if err != nil {
		t.Fatalf("查询活动规则失败：%v", err)
	}
	got := ruleIDs(rules)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("命中 %s，期望 [1 2]：窗口内与恰好开始；恰好结束应失效（end_at > now 是严格大于）", ids(rules))
	}
}

// TestActiveRulesSkipsInactiveAndDeleted 钉住状态与软删除过滤。
//
// 停用和软删除的规则都不该发券。软删除最容易漏——
// 规则删掉了规则行还在，用户看到的活动列表也少了，但发券路径照样能选中它。
func TestActiveRulesSkipsInactiveAndDeleted(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedRule(t, tx, 1, TriggerFirstRecharge, hours(-1), hours(1), 0)
	seedRule(t, tx, 2, TriggerFirstRecharge, hours(-1), hours(1), 0, withStatus("disabled"))
	seedRule(t, tx, 3, TriggerFirstRecharge, hours(-1), hours(1), 0, withDeletedAt(fixedNow))

	rules, err := activeRules(tx, ActivityEvent{TriggerType: TriggerFirstRecharge, Now: fixedNow})
	if err != nil {
		t.Fatalf("查询活动规则失败：%v", err)
	}
	if got := ids(rules); got != "[1]" {
		t.Fatalf("命中 %s，期望 [1]：停用与软删除的规则都不得参与发券", got)
	}
}

// TestActiveRulesThresholdRedeemUsesInclusiveThreshold 钉住满减的门槛判定。
//
// threshold_cents > 0 AND threshold_cents <= amount：门槛相等算达标。
// 差一分不达标会让「满 100 减 10」在正好 100 元的单上不发券；
// 写成 < 则每次都多发一张。
func TestActiveRulesThresholdRedeemUsesInclusiveThreshold(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedRule(t, tx, 1, TriggerThresholdRedeem, hours(-1), hours(1), 5000)
	seedRule(t, tx, 2, TriggerThresholdRedeem, hours(-1), hours(1), 10000)
	seedRule(t, tx, 3, TriggerThresholdRedeem, hours(-1), hours(1), 0) // 门槛为零等于无条件发券

	t.Run("金额等于门槛时达标", func(t *testing.T) {
		rules, err := activeRules(tx, ActivityEvent{
			TriggerType: TriggerThresholdRedeem, AmountCents: 10000, Now: fixedNow,
		})
		if err != nil {
			t.Fatalf("查询失败：%v", err)
		}
		if got := ids(rules); got != "[1 2]" {
			t.Fatalf("命中 %s，期望 [1 2]：门槛等于金额算达标", got)
		}
	})

	t.Run("金额低于门槛时不达标", func(t *testing.T) {
		rules, err := activeRules(tx, ActivityEvent{
			TriggerType: TriggerThresholdRedeem, AmountCents: 5000, Now: fixedNow,
		})
		if err != nil {
			t.Fatalf("查询失败：%v", err)
		}
		if got := ids(rules); got != "[1]" {
			t.Fatalf("命中 %s，期望 [1]：金额 5000 低于门槛 10000", got)
		}
	})

	t.Run("门槛为零的规则永不发券", func(t *testing.T) {
		rules, err := activeRules(tx, ActivityEvent{
			TriggerType: TriggerThresholdRedeem, AmountCents: 1_000_000, Now: fixedNow,
		})
		if err != nil {
			t.Fatalf("查询失败：%v", err)
		}
		if got := ids(rules); got != "[1 2]" {
			t.Fatalf("命中 %s，期望 [1 2]：门槛为 0 的满减规则必须被过滤掉", got)
		}
	})
}

// TestActiveRulesThresholdNotAppliedToOtherTriggers 钉住门槛只对满减生效。
//
// 门槛条件挂在 trigger_type = 'threshold_redeem' 的分支里。
// 若被误用到所有触发类型上，首充和节日活动会在金额为 0 时全不发券。
func TestActiveRulesThresholdNotAppliedToOtherTriggers(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedRule(t, tx, 1, TriggerFirstRecharge, hours(-1), hours(1), 0)
	seedRule(t, tx, 2, TriggerHoliday, hours(-1), hours(1), 0)

	rules, err := activeRules(tx, ActivityEvent{
		TriggerType: TriggerFirstRecharge, AmountCents: 0, Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if got := ids(rules); got != "[1]" {
		t.Fatalf("命中 %s，期望 [1]：金额为 0 时首充活动不该被门槛拦下", got)
	}
}

// TestActiveRulesIsolatesTriggerTypes 钉住触发类型之间互不串场。
//
// 一次充值事件不应触发首充以外的规则；漏掉 trigger_type 条件，
// 一个事件会把所有活动的券一次性发完。
func TestActiveRulesIsolatesTriggerTypes(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	for i, trigger := range []string{TriggerFirstRecharge, TriggerInviteReward, TriggerThresholdRedeem, TriggerHoliday} {
		seedRule(t, tx, uint64(i+1), trigger, hours(-1), hours(1), 100)
	}

	for i, trigger := range []string{TriggerFirstRecharge, TriggerInviteReward, TriggerThresholdRedeem, TriggerHoliday} {
		t.Run(trigger, func(t *testing.T) {
			rules, err := activeRules(tx, ActivityEvent{
				TriggerType: trigger, AmountCents: 1_000_000, Now: fixedNow,
			})
			if err != nil {
				t.Fatalf("查询失败：%v", err)
			}
			if want := fmt.Sprint([]uint64{uint64(i + 1)}); ids(rules) != want {
				t.Errorf("%s 事件命中 %s，期望只有 %s", trigger, ids(rules), want)
			}
		})
	}
}

// TestActiveRulesOrdersByID 钉住发放顺序。
//
// 多条规则同时命中时按 id 升序发放。顺序变了会让用户看到的入账顺序
// 与活动配置顺序对不上，客服对账时无从解释。
func TestActiveRulesOrdersByID(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedRule(t, tx, 1, TriggerHoliday, hours(-1), hours(1), 0)
	seedRule(t, tx, 2, TriggerHoliday, hours(-1), hours(1), 0)
	seedRule(t, tx, 3, TriggerHoliday, hours(-1), hours(1), 0)

	rules, err := activeRules(tx, ActivityEvent{TriggerType: TriggerHoliday, Now: fixedNow})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if got := ids(rules); got != "[1 2 3]" {
		t.Fatalf("命中顺序 %s，期望 [1 2 3]", got)
	}
}

// TestActiveRulesProjectsOnlyRuleFields 钉住投影字段。
//
// 投影漏一列不会编译失败，只会让那段判定读到零值：
// max_grants 读成 0 意味着"不限量"，per_user_limit 读成 0 意味着"按 1 份"，
// 两种都会在发券时产生与预期不同的结果。
func TestActiveRulesProjectsOnlyRuleFields(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedRule(t, tx, 7, TriggerInviteReward, hours(-1), hours(1), 0,
		withPerUserLimit(3), withInviterCoupon(8888))

	rules, err := activeRules(tx, ActivityEvent{
		TriggerType: TriggerInviteReward, InviterID: 5, Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("命中 %d 条，期望 1 条", len(rules))
	}
	r := rules[0]
	switch {
	case r.ID != 7:
		t.Errorf("ID = %d，期望 7", r.ID)
	case r.TriggerType != TriggerInviteReward:
		t.Errorf("TriggerType = %q", r.TriggerType)
	case r.CouponID != 900007:
		t.Errorf("CouponID = %d，期望 900007", r.CouponID)
	case !r.InviterCouponID.Valid || r.InviterCouponID.Int64 != 8888:
		t.Errorf("InviterCouponID = %+v，期望 8888", r.InviterCouponID)
	case r.PerUserLimit != 3:
		t.Errorf("PerUserLimit = %d，期望 3（读成 0 会被当成按 1 份）", r.PerUserLimit)
	case r.Status != "active":
		t.Errorf("Status = %q", r.Status)
	case !r.StartAt.Equal(hours(-1)):
		t.Errorf("StartAt = %v", r.StartAt)
	case !r.EndAt.Equal(hours(1)):
		t.Errorf("EndAt = %v", r.EndAt)
	}
}

// TestActiveRulesReadsMaxGrantsZeroAsUnlimited 钉住"0 表示不限量"。
//
// 投影必须如实读出 max_grants=0，判定层才把它当不限量。
// 若查询写成 COALESCE(max_grants, 1) 之类的兜底，预算会从"不限"变成"限 1 份"。
func TestActiveRulesReadsMaxGrantsZeroAsUnlimited(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedRule(t, tx, 1, TriggerHoliday, hours(-1), hours(1), 0)

	rules, err := activeRules(tx, ActivityEvent{TriggerType: TriggerHoliday, Now: fixedNow})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("命中 %d 条，期望 1 条", len(rules))
	}
	if rules[0].MaxGrants != 0 {
		t.Errorf("MaxGrants = %d，期望 0（0 表示不限量，不是 1）", rules[0].MaxGrants)
	}
}
