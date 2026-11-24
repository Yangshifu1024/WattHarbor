//go:build contract

package serialization

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"wattharbor/internal/marketing"
)

// 本文件锁定 discount_percent 在两端的不同 JSON 形状。
//
// 这是契约里一处容易被忽略、且改动风险真实存在的差异：
// 后台侧用 *decimal.Decimal，序列化为**带引号的字符串**（"80"）；
// C 端用 *float64，序列化为**裸数字**（80）。
// 同一个业务字段两端类型不同，若有人「统一」其中一侧的字段类型，
// 就会静默改变对外 JSON 的类型（前端拿到 number 而非 string，或反之）。
//
// 因此这里对两侧分别断言精确字节，锁住引号的有无。

// TestDiscountPercentAdminIsString 锁定后台侧：decimal 序列化为字符串。
func TestDiscountPercentAdminIsString(t *testing.T) {
	whole := decimal.NewFromInt(80)
	half := decimal.NewFromFloat(12.5)
	assertEach(t, []struct {
		Name  string
		Value marketing.AdminCouponRow
		Want  string
	}{
		{"percent_80_输出字符串", marketing.AdminCouponRow{
			DiscountType:    marketing.DiscountPercentage,
			DiscountPercent: &whole,
		}, `{"id":0,"name":"","discount_type":"percentage","discount_value_cents":null,"discount_percent":"80","free_minutes":null,"min_charge_cents":0,"valid_hours":0,"total_quota":0,"per_user_quota":0,"status":"","start_at":null,"end_at":null}`},
		{"percent_12.5_保留小数", marketing.AdminCouponRow{
			DiscountType:    marketing.DiscountPercentage,
			DiscountPercent: &half,
		}, `{"id":0,"name":"","discount_type":"percentage","discount_value_cents":null,"discount_percent":"12.5","free_minutes":null,"min_charge_cents":0,"valid_hours":0,"total_quota":0,"per_user_quota":0,"status":"","start_at":null,"end_at":null}`},
		{"percent_为空时输出null", marketing.AdminCouponRow{
			DiscountType: marketing.DiscountPercentage,
		}, `{"id":0,"name":"","discount_type":"percentage","discount_value_cents":null,"discount_percent":null,"free_minutes":null,"min_charge_cents":0,"valid_hours":0,"total_quota":0,"per_user_quota":0,"status":"","start_at":null,"end_at":null}`},
	})

	// 后台入参 Coupon 走同一形状：decimal → 字符串。
	whole2 := decimal.NewFromInt(95)
	assertEach(t, []struct {
		Name  string
		Value marketing.Coupon
		Want  string
	}{
		{"input_percent_95", marketing.Coupon{
			DiscountType:    marketing.DiscountPercentage,
			DiscountPercent: &whole2,
		}, `{"id":0,"name":"","discount_type":"percentage","discount_value_cents":null,"discount_percent":"95","free_minutes":null,"min_charge_cents":0,"valid_hours":0,"total_quota":0,"per_user_quota":0,"status":"","start_at":null,"end_at":null}`},
	})
}

// TestDiscountPercentUserIsNumber 锁定 C 端：float64 序列化为裸数字。
//
// 与后台侧的 "80"（字符串）逐字节不同，两者不可互相推断。
func TestDiscountPercentUserIsNumber(t *testing.T) {
	eighty := 80.0
	assertEach(t, []struct {
		Name  string
		Value marketing.UserCoupon
		Want  string
	}{
		{"user_percent_80_输出裸数字", marketing.UserCoupon{
			DiscountType:    marketing.DiscountPercentage,
			DiscountPercent: &eighty,
		}, `{"coupon_id":0,"name":"","grant_id":0,"discount_type":"percentage","discount_value_cents":null,"discount_percent":80,"min_charge_cents":0,"status":"","expired_at":"0001-01-01T00:00:00Z","expires_at":"0001-01-01T00:00:00Z","usable":false}`},
		{"user_percent_为空时输出null", marketing.UserCoupon{
			DiscountType: marketing.DiscountPercentage,
		}, `{"coupon_id":0,"name":"","grant_id":0,"discount_type":"percentage","discount_value_cents":null,"discount_percent":null,"min_charge_cents":0,"status":"","expired_at":"0001-01-01T00:00:00Z","expires_at":"0001-01-01T00:00:00Z","usable":false}`},
	})
}

// TestDiscountPercent两端类型不同 并排锁定，引号有无是契约的一部分。
func TestDiscountPercent两端类型不同(t *testing.T) {
	whole := decimal.NewFromInt(80)
	f := 80.0
	admin, err := json.Marshal(marketing.AdminCouponRow{
		DiscountType: marketing.DiscountPercentage, DiscountPercent: &whole,
	})
	if err != nil {
		t.Fatalf("后台序列化失败：%v", err)
	}
	user, err := json.Marshal(marketing.UserCoupon{
		DiscountType: marketing.DiscountPercentage, DiscountPercent: &f,
	})
	if err != nil {
		t.Fatalf("C 端序列化失败：%v", err)
	}
	if !strings.Contains(string(admin), `"discount_percent":"80"`) {
		t.Errorf("后台侧应为带引号的字符串，实际：%s", admin)
	}
	if !strings.Contains(string(user), `"discount_percent":80`) {
		t.Errorf("C 端应为裸数字，实际：%s", user)
	}
}
