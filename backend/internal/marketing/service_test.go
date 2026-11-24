package marketing

import (
	"context"
	"errors"
	"github.com/shopspring/decimal"
	"testing"
	"time"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/ops"
)

// TestMarketingAdmissionBeforeStorage 验证权限和优惠互斥检查在 SQL 执行前拒绝非法请求。
func TestMarketingAdmissionBeforeStorage(t *testing.T) {
	ctx := context.Background()
	if _, err := (CouponService{}).List(ctx, operations.Profile{}); !errors.Is(err, operations.ErrForbidden) {
		t.Fatal(err)
	}
	audit := ops.AuditContext{Actor: operations.Profile{Permissions: []string{"coupon.create", "coupon.activity.manage"}}}
	amount := int64(500)
	minutes := uint32(30)
	percent := decimal.RequireFromString("12.345")
	base := Coupon{Name: "券", DiscountType: "amount", DiscountValueCents: &amount, ValidHours: 24, PerUserQuota: 1}
	for _, change := range []func(*Coupon){func(c *Coupon) { c.ID = 1 }, func(c *Coupon) { c.PerUserQuota = 0 }, func(c *Coupon) { c.ValidHours = 87601 }, func(c *Coupon) { c.FreeMinutes = &minutes }, func(c *Coupon) {
		c.DiscountType = "percentage"
		c.DiscountValueCents = nil
		c.DiscountPercent = &percent
	}} {
		in := base
		change(&in)
		_, err := (CouponService{}).Create(ctx, audit, in)
		var input *InputError
		if !errors.As(err, &input) {
			t.Fatal("invalid coupon reached storage", err)
		}
	}
	_, err := (ActivityService{}).Create(ctx, audit, ActivityInput{Name: "invalid", TriggerType: TriggerHoliday, CouponID: 1, StartAt: time.Now().Format(time.RFC3339), EndAt: "invalid"})
	var input *InputError
	if !errors.As(err, &input) {
		t.Fatal(err)
	}
}

// TestCouponDiscountUsesOriginalPrice 验证百分比只按原价计算一次且只做一次整分舍入。
func TestCouponDiscountUsesOriginalPrice(t *testing.T) {
	percent := 75.0
	got, err := discountFor(CouponGrant{Type: "percentage", Percent: &percent}, 1001)
	if err != nil || got != 250 {
		t.Fatalf("got %d %v", got, err)
	}
	amount := int64(500)
	got, err = discountFor(CouponGrant{Type: "amount", Amount: &amount}, 300)
	if err != nil || got != 300 {
		t.Fatalf("capped %d %v", got, err)
	}
}
