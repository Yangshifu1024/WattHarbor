package marketing

import (
	"errors"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func activeCoupon() CouponTemplate {
	return CouponTemplate{
		TemplateID: 7, Status: "active", TotalQuota: 0, PerUserQuota: 2, ValidHours: 24,
	}
}

func TestCheckGrantableRejectsEveryBlockingCondition(t *testing.T) {
	now := at("2026-10-02T12:00:00Z")
	start := at("2026-10-01T00:00:00Z")
	end := at("2026-10-03T00:00:00Z")

	ok := activeCoupon()
	ok.StartAt, ok.EndAt = &start, &end
	if err := CheckGrantable(now, ok, CouponUsage{}); err != nil {
		t.Fatalf("有效期内应可发放：%v", err)
	}
	no := CouponUsage{}
	for name, mutate := range map[string]func(*CouponTemplate, *CouponUsage){
		"模板停用":      func(c *CouponTemplate, _ *CouponUsage) { c.Status = "disabled" },
		"尚未生效":      func(c *CouponTemplate, _ *CouponUsage) { s := at("2026-10-05T00:00:00Z"); c.StartAt = &s },
		"已过结束":      func(c *CouponTemplate, _ *CouponUsage) { e := at("2026-10-01T00:00:00Z"); c.EndAt = &e },
		"总量发完":      func(c *CouponTemplate, u *CouponUsage) { c.TotalQuota = 5; u.TotalGranted = 5 },
		"该user已达上限": func(_ *CouponTemplate, u *CouponUsage) { u.UserGranted = 2 },
	} {
		c := ok
		u := no
		mutate(&c, &u)
		if err := CheckGrantable(now, c, u); !errors.Is(err, ErrCouponGrant) {
			t.Fatalf("%s 应拒绝发放，实得 %v", name, err)
		}
	}
}

func TestCheckGrantableTreatsZeroTotalQuotaAsUnlimited(t *testing.T) {
	now := at("2026-10-02T12:00:00Z")
	// TotalQuota=0 表示不限量，已发一万份仍应可发；PerUserQuota 仍照常生效。
	if err := CheckGrantable(now, activeCoupon(), CouponUsage{TotalGranted: 10000}); err != nil {
		t.Fatalf("不限量却被拒：%v", err)
	}
}

func TestGrantExpiryIsCappedByCouponEnd(t *testing.T) {
	now := at("2026-10-02T12:00:00Z")
	// 24 小时后到期，券本身 6 小时后结束 → 以 6 小时封顶
	end := at("2026-10-02T18:00:00Z")
	c := activeCoupon()
	c.EndAt = &end
	if got, want := GrantExpiry(now, c), end; !got.Equal(want) {
		t.Fatalf("到期未按券结束时间封顶：%s want %s", got, want)
	}
	// 券结束时间很晚时，按 valid_hours 计算
	later := at("2026-10-30T00:00:00Z")
	c.EndAt = &later
	if got, want := GrantExpiry(now, c), at("2026-10-03T12:00:00Z"); !got.Equal(want) {
		t.Fatalf("到期算错：%s want %s", got, want)
	}
	// 无结束时间的长期券：纯按 valid_hours
	c.EndAt = nil
	if got, want := GrantExpiry(now, c), at("2026-10-03T12:00:00Z"); !got.Equal(want) {
		t.Fatalf("长期券到期算错：%s want %s", got, want)
	}
}

func TestReplayGrantOnlyReusesSameRequestForSameCouponAndUser(t *testing.T) {
	receipt := CouponGrantReceipt{RequestID: "r-1", CouponID: 7, UserID: 9001, CouponGrantID: 555}

	// 完全一致 → 沿用首次凭据
	got, err := ReplayGrant(receipt, "r-1", 7, 9001)
	if err != nil || got != 555 {
		t.Fatalf("一致重放应回首次凭据：%d %v", got, err)
	}
	// 券或用户变了 → 拒绝，绝不能把 A 券的凭据发给 B 用户
	for name, args := range map[string][2]uint64{
		"换券":  {8, 9001},
		"换用户": {7, 9002},
	} {
		if _, err := ReplayGrant(receipt, "r-1", args[0], args[1]); !errors.Is(err, ErrCouponGrant) {
			t.Fatalf("%s 应判冲突，实得 %v", name, err)
		}
	}
	if _, err := ReplayGrant(receipt, "r-2", 7, 9001); !errors.Is(err, ErrCouponGrant) {
		t.Fatal("请求号不符应判冲突")
	}
}
