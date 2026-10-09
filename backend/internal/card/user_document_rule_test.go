package card_test

import (
	"testing"

	cardstore "chargepilot/internal/card"
	charging "chargepilot/internal/domain/charging"
)

// TestUserDocumentCardAdditionRule 用 t 验证卡续加的纯准入规则与文档金额示例，不执行资金事务或 HTTP 请求。
func TestUserDocumentCardAdditionRule(t *testing.T) {
	p := charging.Package{ID: 1, Name: "时长", Mode: "duration", Minutes: 120, PriceCents: 200}
	for _, tc := range []struct {
		// name 该子测试的场景名称。
		name string
		// card 场景中的卡身份，与当前卡身份比较。
		card uint64
		// purchased,max 已购买时长与允许上限，单位为分钟。
		purchased, max uint16
		// balance 场景中可用余额，单位为分。
		balance int64
		// pending,want 是否有未知结果的扣款与预期准入结果。
		pending, want bool
	}{
		{"480 to 600", 1, 480, 600, 200, false, true}, {"reject 720", 1, 600, 600, 1000, false, false}, {"insufficient wallet", 1, 120, 600, 100, false, false}, {"different card", 2, 120, 600, 1000, false, false}, {"unknown result prevents another debit", 1, 120, 600, 1000, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cardstore.CanExtend(tc.card, 1, p, tc.purchased, tc.max, tc.balance, tc.pending); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}
