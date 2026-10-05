package marketing

import (
	"testing"
	"time"
)

// TestActivityGrantExpiryTakesEarliestOfThreeBounds 钉住活动发券的到期时间。
//
// 三个上界取最早：券有效期、券自身结束时间、活动结束时间。
// 少算一个上界就会发出活动收尾后仍能核销的券——单测无法发现这种券，
// 只能等核销台账和预算对不上时才暴露。
func TestActivityGrantExpiryTakesEarliestOfThreeBounds(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return now.Add(time.Duration(h) * time.Hour) }

	// 三个上界分别取 24 / 48 / 10 小时，只有逐个把它们设成最短的那一项，
	// 期望值才互不相同——若三个上界同时存在却让某一项永远赢不了，
	// 少算一个上界的错误就测不出来。
	couponEnd, activityEnd := at(48), at(10)

	cases := []struct {
		// name 当前边界场景名称，供 t.Run 区分断言；不作为业务身份或数据库条件。
		name string
		// validHours 券从发放时刻起算的有效期，单位小时；零用来验证立即到期边界。
		validHours int
		// couponEnd 券模板的可选绝对结束时间；nil 表示无模板结束限制。
		couponEnd *time.Time
		// activityEn 活动的绝对结束时间，与券有效期和模板结束时间共同取最早期限。
		activityEn time.Time
		// want 当前规则组合预期的最早发券到期时刻。
		want time.Time
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

// TestPerUserAllowanceTreatsUnsetAsOne 钉住单人份数的归一化。
//
// 0 与负数都必须落到 1 份，不能当成不限量。不限量的语义由总发放上限承担；
// 若单人额度被当成不限量，一次活动就能把全部预算发给同一个人。
func TestPerUserAllowanceTreatsUnsetAsOne(t *testing.T) {
	cases := map[string]struct {
		// in,want in 为配置的单人份数，want 为预期有效份数；零或负数预期使用一份，不当作不限量。
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

// TestInviteEligibleRejectsSelfInviteAndEmptyInviter 钉住邀请有奖的发放前提。
//
// 自邀与空邀请人会让两个（可能是空）账号成对消耗活动预算而不产生真实用户。
// 非邀请类触发不受邀请关系影响——把它们一并拒绝会误伤首充与节日活动。
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
		for _, trigger := range []string{TriggerFirstRecharge, TriggerThresholdRedeem, TriggerHoliday} {
			if !InviteEligible(trigger, 0, user) {
				t.Errorf("%s 不依赖邀请关系，邀请人为空也应照常判定", trigger)
			}
			if !InviteEligible(trigger, user, user) {
				t.Errorf("%s 不依赖邀请关系，自指不应被当成自邀", trigger)
			}
		}
	})
}

// TestActivityEventIDIsStableAndScopeSeparated 钉住发放幂等键。
//
// 幂等键由规则、用户、事件键与 scope 派生：网络重试必须算出同一个键（否则
// 重试会重复发券），而邀请人与被邀请人的奖励必须算出不同的键（否则一方拿到
// 奖励会顶掉另一方的那张券）。
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

// TestGrantSourceForMapsOnlyInviteReward 钉住发放来源的映射。
//
// coupon_grant 的 grant_source 决定 countRuleGrants 按哪一档统计已发放量，
// 映射错了会让满减活动的预算统计把别人的券算进来，提前耗尽。
func TestGrantSourceForMapsOnlyInviteReward(t *testing.T) {
	if got := grantSourceFor(TriggerInviteReward); got != "invite_reward" {
		t.Errorf("邀请有奖应记为 invite_reward，实际 %q", got)
	}
	for _, trigger := range []string{TriggerFirstRecharge, TriggerThresholdRedeem, TriggerHoliday} {
		if got := grantSourceFor(trigger); got != "activity" {
			t.Errorf("%s 应记为 activity，实际 %q", trigger, got)
		}
	}
}

// TestApplyActivityRulesRejectsIncompleteEvent 钉住事件入参的前置校验。
//
// 这三项缺失任何一项都算不出可用的幂等键：用户为空会发到不存在的账户，
// 事件键为空会让所有重试撞同一个键，Now 为零会让活动窗口判定恒为假。
// 校验在触碰事务之前完成，所以这里不需要数据库。
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
			// tx 传 nil 是有意的：校验必须发生在任何数据库访问之前，
			// 一旦这个前提被打破，这里会先 panic 而不是返回业务错误。
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

// TestActivityEventKeysArePrefixedAndStable 钉住两个事件键构造器的格式。
//
// 前缀把不同来源的触发隔开：同一笔充值与同一笔订单若共用一个键，
// 幂等约束会误判为同一次事件，少发一张券。
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
