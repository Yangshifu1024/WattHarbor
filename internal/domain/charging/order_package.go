package charging

import (
	"encoding/json"
	"strings"
)

// 订单冻结的计费口径。订单展示与结算都必须读这份快照，
// 绝不能去读当前的站点方案重算——站点方案随时可能被改、被停用、被删，
// 改了之后历史订单的收费依据不能跟着变。
//
// 本文件只负责从快照还原口径与取出展示名称；取快照是持久化层的事。

// OrderPackage 是订单在创建支付意图时冻结下来的完整计费口径。
type OrderPackage struct {
	Offer       Offer              `json:"offer"`
	Scheme      *Scheme            `json:"scheme,omitempty"`
	BillingMode ChargeMode         `json:"billing_mode"`
	RuleVersion uint32             `json:"rule_version"`
	Card        *OrderCardPurchase `json:"card,omitempty"`
}

// OrderCardPurchase 是随单冻结的刷卡购买信息。首次刷卡之后仍可能续时，
// 因此原始单价档保持不变，累计时长单独记在 PurchasedMinutes。
type OrderCardPurchase struct {
	PaidCents        int64  `json:"paid_cents"`
	PurchasedMinutes uint16 `json:"purchased_minutes"`
	MaxMinutes       uint16 `json:"max_minutes"`
}

// PackageFromSnapshot 从订单的定价快照还原冻结口径。
// 快照结构不符、缺少 offer 或 offer 本身不合法时返回 nil——
// 宁可显式表示"读不出"，也不用当前模板补一个看似合理的值。
func PackageFromSnapshot(raw []byte) *OrderPackage {
	var frozen struct {
		Rule  Rule   `json:"rule"`
		Offer *Offer `json:"offer"`
	}
	if json.Unmarshal(raw, &frozen) != nil || frozen.Offer == nil || !frozen.Offer.Valid() {
		return nil
	}
	return &OrderPackage{Offer: *frozen.Offer, Scheme: frozen.Rule.Spec.Scheme,
		BillingMode: frozen.Rule.Spec.Mode, RuleVersion: frozen.Rule.Version}
}

// SelectedSchemeName 读冻结方案名称。
// 方案名为空时返回 nil，不会退而用 offer 的价格档名称顶替——
// 那是两样东西，混起来会让运营看到"方案"却其实是套餐。
func SelectedSchemeName(selected *OrderPackage) *string {
	if selected == nil || selected.Scheme == nil {
		return nil
	}
	name := strings.TrimSpace(selected.Scheme.Name)
	if name == "" {
		return nil
	}
	return &name
}

// SelectedPackageName 读冻结的价格档名称。
func SelectedPackageName(selected *OrderPackage) *string {
	if selected == nil {
		return nil
	}
	name := strings.TrimSpace(selected.Offer.Name)
	if name == "" {
		return nil
	}
	return &name
}
