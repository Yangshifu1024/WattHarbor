package order

import (
	"testing"
)

// TestPaymentTimeRangeAcceptsEmptyOpenAndEqualBounds 验证空界、单侧界与相同时刻均可解析，时区差异不改变实际边界。
func TestPaymentTimeRangeAcceptsEmptyOpenAndEqualBounds(t *testing.T) {
	start := "2026-10-01T08:00:00+08:00"
	for _, tc := range []struct {
		// from、to 是原查询起止文本，空文本表示无此侧边界。
		from, to string
		// hasFrom、hasTo 是解析后两侧是否应返回非nil时刻。
		hasFrom, hasTo bool
	}{
		{"", "", false, false}, {start, "", true, false}, {"", start, false, true}, {start, start, true, true},
		{start, "2026-10-01T00:00:00Z", true, true},
	} {
		from, to, err := paymentTimeRange(tc.from, tc.to)
		if err != nil || (from != nil) != tc.hasFrom || (to != nil) != tc.hasTo {
			t.Fatalf("%+v %v %v %v", tc, from, to, err)
		}
		if from != nil && to != nil && !from.Equal(*to) {
			t.Fatal("same instant has different bounds", from, to)
		}
	}
}

// TestPaymentTimeRangeRejectsInvalidAndReversedBounds 验证非法时刻、倒置范围及超出数据库时刻范围的输入返回错误。
func TestPaymentTimeRangeRejectsInvalidAndReversedBounds(t *testing.T) {
	for _, tc := range [][2]string{
		{"invalid", ""}, {"", "2026-10-01"}, {"2026-10-02T00:00:00Z", "2026-10-01T00:00:00Z"},
		{"0001-01-01T00:00:00Z", ""}, {"9999-12-31T23:59:59-08:00", ""},
	} {
		if _, _, err := paymentTimeRange(tc[0], tc[1]); err == nil {
			t.Fatal("accepted", tc)
		}
	}
}
