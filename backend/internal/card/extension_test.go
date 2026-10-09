package card

import (
	"errors"
	"testing"

	"chargepilot/internal/domain/charging"
	"chargepilot/internal/wallet"
)

// TestCanExtendRequiresEveryGuard 验证续购的卡标识、套餐、金额、时长、待确认状态、余额及上限条件。
func TestCanExtendRequiresEveryGuard(t *testing.T) {
	duration := charging.Package{ID: 1, Mode: "duration", PriceCents: 500, Minutes: 30}
	base := func() (uint64, uint64, charging.Package, uint16, uint16, int64, bool) {
		return 7, 7, duration, 0, 120, 1000, false
	}

	card, orderCard, p, purchased, max, balance, pending := base()
	if !CanExtend(card, orderCard, p, purchased, max, balance, pending) {
		t.Fatal("全部条件成立时应允许加时")
	}

	cases := []struct {
		name   string
		break_ func(*uint64, *uint64, *charging.Package, *uint16, *uint16, *int64, *bool)
	}{
		{"卡与订单不匹配", func(c, oc *uint64, _ *charging.Package, _, _ *uint16, _ *int64, _ *bool) { *oc = 8 }},
		{"非时长套餐", func(_, _ *uint64, pp *charging.Package, _, _ *uint16, _ *int64, _ *bool) { pp.Mode = "energy" }},
		{"套餐无价", func(_, _ *uint64, pp *charging.Package, _, _ *uint16, _ *int64, _ *bool) { pp.PriceCents = 0 }},
		{"套餐无分钟", func(_, _ *uint64, pp *charging.Package, _, _ *uint16, _ *int64, _ *bool) { pp.Minutes = 0 }},
		{"有进行中的刷卡", func(_, _ *uint64, _ *charging.Package, _, _ *uint16, _ *int64, pd *bool) { *pd = true }},
		{"余额不足", func(_, _ *uint64, _ *charging.Package, _, _ *uint16, b *int64, _ *bool) { *b = 499 }},
		{"加完超上限", func(_, _ *uint64, _ *charging.Package, pu *uint16, _ *uint16, _ *int64, _ *bool) { *pu = 100 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card, orderCard, p, purchased, max, balance, pending := base()
			tc.break_(&card, &orderCard, &p, &purchased, &max, &balance, &pending)
			if CanExtend(card, orderCard, p, purchased, max, balance, pending) {
				t.Fatal("条件不成立却允许加时，会产生不该发生的扣款")
			}
		})
	}
}

// TestCanExtendBoundaries 确认边界：余额正好等于价格、已购时长加完正好到上限。
// 都应允许；差一分都不行。
func TestCanExtendBoundaries(t *testing.T) {
	p := charging.Package{ID: 1, Mode: "duration", PriceCents: 500, Minutes: 30}
	if !CanExtend(7, 7, p, 90, 120, 500, false) {
		t.Error("余额恰好等于价格、已购 90 加 30 恰好到 120，应允许")
	}
	if CanExtend(7, 7, p, 91, 120, 500, false) {
		t.Error("已购 91 加 30 超过上限 120，应拒绝")
	}
	if CanExtend(7, 7, p, 0, 120, 499, false) {
		t.Error("余额比价格少一分，应拒绝")
	}
}

// TestMapWalletMoveError 验证余额不足与账户不可用映射为刷卡错误，其他错误保留原因。
func TestMapWalletMoveError(t *testing.T) {
	if got := mapWalletMoveError(wallet.ErrInsufficient); !errors.Is(got, ErrCardBalance) {
		t.Errorf("余额不足应映射为 ErrCardBalance，实际 %v", got)
	}
	if got := mapWalletMoveError(wallet.ErrInactive); !errors.Is(got, ErrCardOperation) {
		t.Errorf("账户非活跃应映射为 ErrCardOperation，实际 %v", got)
	}
	// 其它错误原样透传，不能被改写成刷卡哨兵——那会掩盖真实原因。
	other := errors.New("渠道超时")
	if got := mapWalletMoveError(other); !errors.Is(got, other) {
		t.Errorf("无关错误应原样透传，实际 %v", got)
	}
	if got := mapWalletMoveError(nil); got != nil {
		t.Errorf("nil 应返回 nil，实际 %v", got)
	}
}
