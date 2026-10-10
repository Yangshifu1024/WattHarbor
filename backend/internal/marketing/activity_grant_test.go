package marketing

import (
	"testing"
	"time"
)

// TestActivityGrantExpiryTakesEarliestOfThreeBounds 验证到期时间取券有效期、模板结束和活动结束中的最早值。
func TestActivityGrantExpiryTakesEarliestOfThreeBounds(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return now.Add(time.Duration(h) * time.Hour) }

	// 让三个截止点分别成为最早值，确保遗漏任一上限都能使测试失败。
	couponEnd, activityEnd := at(48), at(10)

	cases := []struct {
		name string
		// validHours 是自发券起算的有效小时数，零用于立即到期边界。
		validHours int
		// couponEnd 是模板结束时间，nil 表示不限制。
		couponEnd *time.Time
		// activityEn 是活动结束时间，也是发券有效期上限。
		activityEn time.Time
		want       time.Time
	}{
		{"券有效期最短", 24, &couponEnd, at(72), at(24)},
		{"券结束时间最短", 72, &couponEnd, at(72), at(48)},
		{"活动结束时间最短", 72, &couponEnd, at(10), at(10)},
		{"券无结束时间时由有效期决定", 24, nil, at(72), at(24)},
		{"券无结束时间时由活动结束时间决定", 72, nil, at(10), at(10)},
		{"有效期与活动结束时间相同", 10, &couponEnd, at(10), at(10)},
		{"零小时有效期立即到期", 0, &couponEnd, at(72), now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ActivityGrantExpiry(now, tc.validHours, tc.couponEnd, tc.activityEn); !got.Equal(tc.want) {
				t.Errorf("到期时间 = %v，期望 %v（券有效期 %dh、券结束 %v、活动结束 %v）",
					got, tc.want, tc.validHours, tc.couponEnd, tc.activityEn)
			}
		})
	}

	// 活动结束时间是硬上限：无论券自身能用到什么时候，都不得越过它。
	t.Run("任何上界都不得越过活动结束时间", func(t *testing.T) {
		far := at(24 * 365)
		if got := ActivityGrantExpiry(now, 24*365, &far, activityEnd); got.After(activityEnd) {
			t.Errorf("到期时间 %v 越过活动结束时间 %v", got, activityEnd)
		}
	})
}

// TestPerUserAllowanceTreatsUnsetAsOne 验证单人份数为零或负数时按一份处理，不表示不限量。
func TestPerUserAllowanceTreatsUnsetAsOne(t *testing.T) {
	cases := map[string]struct {
		// in、want 分别为配置和有效份数，非正配置预期为一份。
		in, want int
	}{
		"未配置（0）":   {0, 1},
		"负数":       {-1, 1},
		"配置 1 份":   {1, 1},
		"配置 3 份":   {3, 3},
		"配置 10 份":  {10, 10},
		"极小负数不应放行": {-99, 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := PerUserAllowance(tc.in); got != tc.want {
				t.Errorf("PerUserAllowance(%d) = %d，期望 %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestInviteEligibleRejectsSelfInviteAndEmptyInviter 验证邀请奖励拒绝自邀和空邀请人；其他触发不检查邀请关系。
func TestInviteEligibleRejectsSelfInviteAndEmptyInviter(t *testing.T) {
	const self, inviter, user = uint64(7), uint64(8), uint64(9)

	t.Run("邀请有奖-自邀被拒", func(t *testing.T) {
		if InviteEligible(TriggerInviteReward, user, user) {
			t.Error("自己邀请自己应拒绝发放")
		}
	})

	t.Run("邀请有奖-空邀请人被拒", func(t *testing.T) {
		if InviteEligible(TriggerInviteReward, 0, user) {
			t.Error("没有邀请人却发放双方奖励，等于凭空造一笔预算消耗")
		}
	})

	t.Run("邀请有奖-真实邀请人放行", func(t *testing.T) {
		if !InviteEligible(TriggerInviteReward, inviter, user) {
			t.Error("正常邀请关系应允许发放")
		}
	})

	t.Run("非邀请类触发不受邀请关系影响", func(t *testing.T) {
		for _, trigger := range []TriggerType{TriggerFirstRecharge, TriggerThresholdRedeem, TriggerHoliday} {
			if !InviteEligible(trigger, 0, user) {
				t.Errorf("%s 不依赖邀请关系，邀请人为空也应照常判定", trigger)
			}
			if !InviteEligible(trigger, user, user) {
				t.Errorf("%s 不依赖邀请关系，自指不应被当成自邀", trigger)
			}
		}
	})
}

// TestActivityEventIDIsStableAndScopeSeparated 验证重试生成相同发放号，邀请人和被邀请人的奖励号不同。
func TestActivityEventIDIsStableAndScopeSeparated(t *testing.T) {
	base := ActivityEvent{UserID: 42, EventKey: "order:O-1"}

	first := activityEventID(7, base)
	if second := activityEventID(7, base); first != second {
		t.Fatalf("同一事件重算幂等键应相同：%s vs %s", first, second)
	}
	if activityEventID(8, base) == first {
		t.Error("不同规则必须生成不同幂等键，否则两个活动的奖励会互相顶掉")
	}
	if activityEventID(7, ActivityEvent{UserID: 43, EventKey: "order:O-1"}) == first {
		t.Error("不同用户必须生成不同幂等键")
	}
	if activityEventID(7, ActivityEvent{UserID: 42, EventKey: "order:O-2"}) == first {
		t.Error("不同事件键必须生成不同幂等键，同一用户的第二单要能独立领一次")
	}
	if activityEventID(7, base, "inviter") == first {
		t.Error("邀请人奖励与被邀请人奖励必须用不同的幂等键")
	}
	if activityEventID(7, base, "inviter") == activityEventID(7, base, "other") {
		t.Error("不同 scope 必须生成不同幂等键")
	}
	if len(first) != 32 {
		t.Errorf("幂等键应为 32 位十六进制，实际长度 %d", len(first))
	}
}

// TestGrantSourceForMapsOnlyInviteReward 验证邀请奖励使用 invite_reward 来源，其余使用 activity，份数按来源统计。
func TestGrantSourceForMapsOnlyInviteReward(t *testing.T) {
	if got := grantSourceFor(TriggerInviteReward); got != "invite_reward" {
		t.Errorf("邀请有奖应记为 invite_reward，实际 %q", got)
	}
	for _, trigger := range []TriggerType{TriggerFirstRecharge, TriggerThresholdRedeem, TriggerHoliday} {
		if got := grantSourceFor(trigger); got != "activity" {
			t.Errorf("%s 应记为 activity，实际 %q", trigger, got)
		}
	}
}

// TestApplyActivityRulesRejectsIncompleteEvent 验证用户、事件键或时间缺失时，在访问数据库前拒绝。
func TestApplyActivityRulesRejectsIncompleteEvent(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]ActivityEvent{
		"无用户":   {EventKey: "k", Now: now},
		"无事件键":  {UserID: 1, Now: now},
		"无触发时刻": {UserID: 1, EventKey: "k"},
		"三项全空":  {},
	}
	for name, event := range cases {
		t.Run(name, func(t *testing.T) {
			// 故意传 nil 事务，确保输入检查先于数据库访问。
			results, err := (ActivityService{}).ApplyInTx(nil, event)
			if err == nil {
				t.Fatal("入参不完整却通过了校验")
			}
			if results != nil {
				t.Errorf("拒绝时不应返回结果，实际 %+v", results)
			}
		})
	}
}

// TestActivityEventKeysArePrefixedAndStable 验证充值和订单事件前缀不同，且重试不改键。
func TestActivityEventKeysArePrefixedAndStable(t *testing.T) {
	if got := ActivityEventKeyForRecharge("R-1"); got != "recharge:R-1" {
		t.Errorf("充值事件键格式不对：%q", got)
	}
	if got := ActivityEventKeyForOrder("O-1"); got != "order:O-1" {
		t.Errorf("订单事件键格式不对：%q", got)
	}
	if ActivityEventKeyForRecharge("X") == ActivityEventKeyForOrder("X") {
		t.Error("充值与订单事件键不能相同")
	}
	if ActivityEventKeyForRecharge("R-1") != ActivityEventKeyForRecharge("R-1") {
		t.Error("同一请求号重试必须得到同一个事件键")
	}
}
