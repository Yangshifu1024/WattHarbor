//go:build integration

package marketing

import (
	"fmt"
	"testing"
	"time"
)

// 真库测试核对 SQL 活动筛选的时间边界、软删除和金额门槛；纯函数测试无法覆盖这些条件。

// fixedNow 让所有窗口判断与墙上时钟无关。
var fixedNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func hours(n int) time.Time { return fixedNow.Add(time.Duration(n) * time.Hour) }

// TestActiveRulesWindowIsStartInclusiveEndExclusive 验证活动开始时刻生效、结束时刻失效，即 start_at <= now < end_at。
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

// TestActiveRulesSkipsInactiveAndDeleted 验证停用或软删活动不发券。
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

// TestActiveRulesThresholdRedeemUsesInclusiveThreshold 验证金额等于门槛时达标，低于门槛一分时不达标。
func TestActiveRulesThresholdRedeemUsesInclusiveThreshold(t *testing.T) {
	db := testDB(t)
	tx := txRollback(t, db)
	seedRule(t, tx, 1, TriggerThresholdRedeem, hours(-1), hours(1), 5000)
	seedRule(t, tx, 2, TriggerThresholdRedeem, hours(-1), hours(1), 10000)
	seedRule(t, tx, 3, TriggerThresholdRedeem, hours(-1), hours(1), 0) // 非法零门槛，查询必须排除

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

// TestActiveRulesThresholdNotAppliedToOtherTriggers 验证金额门槛仅适用于 threshold_redeem。
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

// TestActiveRulesIsolatesTriggerTypes 验证事件只匹配对应触发类型。
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

// TestActiveRulesOrdersByID 验证多个命中规则按 ID 升序发券。
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

// TestActiveRulesProjectsOnlyRuleFields 验证查询完整读取规则字段，避免漏列被误用为零值。
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

// TestActiveRulesReadsMaxGrantsZeroAsUnlimited 验证 max_grants=0 保持不限量，不被查询改成一份。
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
