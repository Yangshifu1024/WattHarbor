package marketing

import (
	"errors"
	"testing"
	"time"
)

func u64(v uint64) *uint64 { return &v }

func baseActivity() ActivityRule {
	return ActivityRule{
		Name: "首充有礼", TriggerType: TriggerFirstRecharge, CouponID: 3,
		StartAt: "2026-10-01T00:00:00+08:00", EndAt: "2026-11-01T00:00:00+08:00",
	}
}

func TestNormalizeActivityDefaultsStatusToActive(t *testing.T) {
	now := at("2026-10-02T12:00:00Z")
	rule := baseActivity() // Status 留空
	start, end, status, err := NormalizeActivity(rule, now)
	if err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("缺省状态应为 active，实得 %q", status)
	}
	// 时间必须已 UTC 化，不能保留入参的 +08:00 偏移
	if start.Location() != time.UTC || start.Hour() != 16 {
		t.Fatalf("开始时间未 UTC 化：%s", start)
	}
	if end.Location() != time.UTC {
		t.Fatalf("结束时间未 UTC 化：%s", end)
	}
}

func TestNormalizeActivityEnforcesPerTriggerConstraints(t *testing.T) {
	now := at("2026-10-02T12:00:00Z")
	// 满减：门槛必须为正
	r := baseActivity()
	r.TriggerType, r.ThresholdCents = TriggerThresholdRedeem, 0
	if _, _, _, err := NormalizeActivity(r, now); !errors.Is(err, ErrActivityRule) {
		t.Fatal("满减缺门槛应判非法")
	}
	r.ThresholdCents = 1000
	if _, _, _, err := NormalizeActivity(r, now); err != nil {
		t.Fatalf("满减带门槛应通过：%v", err)
	}

	// 邀请有奖：必须配邀请人券
	r = baseActivity()
	r.TriggerType = TriggerInviteReward
	if _, _, _, err := NormalizeActivity(r, now); !errors.Is(err, ErrActivityRule) {
		t.Fatal("邀请有奖缺邀请人券应判非法")
	}
	r.InviterCouponID = u64(4)
	if _, _, _, err := NormalizeActivity(r, now); err != nil {
		t.Fatalf("邀请有奖配齐应通过：%v", err)
	}
	// 配了但为 0 同样非法
	r.InviterCouponID = u64(0)
	if _, _, _, err := NormalizeActivity(r, now); !errors.Is(err, ErrActivityRule) {
		t.Fatal("邀请人券为 0 应判非法")
	}

	// 其余触发类型：一律不接受门槛
	for _, trigger := range []TriggerType{TriggerFirstRecharge, TriggerHoliday} {
		r = baseActivity()
		r.TriggerType, r.ThresholdCents = trigger, 500
		if _, _, _, err := NormalizeActivity(r, now); !errors.Is(err, ErrActivityRule) {
			t.Fatalf("%s 不应接受门槛", trigger)
		}
	}
}

func TestNormalizeActivityRejectsBadWindowAndTrigger(t *testing.T) {
	now := at("2026-10-02T12:00:00Z")
	inviter := u64(4)
	for name, mutate := range map[string]func(*ActivityRule){
		"结束早于开始":      func(r *ActivityRule) { r.EndAt = "2026-09-01T00:00:00+08:00" },
		"起止相同":        func(r *ActivityRule) { r.EndAt = r.StartAt },
		"结束超一年":       func(r *ActivityRule) { r.EndAt = "2028-01-01T00:00:00+08:00" },
		"时间非 RFC3339": func(r *ActivityRule) { r.StartAt = "2026/10/01" },
		"未知触发类型":      func(r *ActivityRule) { r.TriggerType = "birthday" },
		"无券":          func(r *ActivityRule) { r.CouponID = 0 },
		"额度为负":        func(r *ActivityRule) { r.MaxGrants = -1 },
		"人均为负":        func(r *ActivityRule) { r.PerUserLimit = -1 },
		"状态非法":        func(r *ActivityRule) { r.Status = "paused" },
		"名称超长":        func(r *ActivityRule) { r.Name = string(make([]rune, 129)) },
	} {
		rule := baseActivity()
		rule.InviterCouponID = inviter
		mutate(&rule)
		if _, _, _, err := NormalizeActivity(rule, now); !errors.Is(err, ErrActivityRule) {
			t.Fatalf("%s 应判非法", name)
		}
	}
}
