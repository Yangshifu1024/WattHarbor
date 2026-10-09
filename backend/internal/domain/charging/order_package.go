package charging

import (
	"encoding/json"
	"strings"
)

// 订单展示与结算只读取购买时冻结的快照，当前站点方案变化不能影响历史价格。
// 本文件从快照还原计价配置和展示名称，快照读取由仓储负责。

// OrderPackage 是订单在创建支付意图时冻结下来的完整计费规则。
type OrderPackage struct {
	Offer       Offer              `json:"offer"`            // 购买套餐及冻结价格，不能由当前模板替换
	Scheme      *Scheme            `json:"scheme,omitempty"` // 完整冻结计价方案，旧计价规则可能缺失
	BillingMode ChargeMode         `json:"billing_mode"`     // 既有规则的计费执行方式
	RuleVersion uint32             `json:"rule_version"`     // 冻结规则版本
	Card        *OrderCardPurchase `json:"card,omitempty"`   // 卡累计购买记录，非刷卡订单为空
}

// OrderCardPurchase 是随单冻结的刷卡购买信息。首次刷卡之后仍可能续时。
// 因此原始单价档保持不变，累计时长单独记在 PurchasedMinutes。
type OrderCardPurchase struct {
	PaidCents        int64  `json:"paid_cents"`        // 累计实际购买金额，分
	PurchasedMinutes uint16 `json:"purchased_minutes"` // 累计购买分钟，续购后可增加
	MaxMinutes       uint16 `json:"max_minutes"`       // 冻结允许购买分钟上限
}

// PackageFromSnapshot 还原购买时的计价配置。
// 快照损坏、缺少套餐或套餐无效时返回 nil，不能用当前模板补齐。
func PackageFromSnapshot(raw []byte) *OrderPackage {
	var frozen struct {
		// Rule 购买时冻结的计费规则；套餐恢复从中读取方案、计费模式和规则版本，不取当前模板。
		// 快照缺少 rule 时保留零值，不因此补读当前规则。
		Rule  Rule   `json:"rule"`
		Offer *Offer `json:"offer"` // 购买套餐及冻结价格，不能由当前模板替换
	}
	if json.Unmarshal(raw, &frozen) != nil || frozen.Offer == nil || !frozen.Offer.Valid() {
		return nil
	}
	return &OrderPackage{Offer: *frozen.Offer, Scheme: frozen.Rule.Spec.Scheme,
		BillingMode: frozen.Rule.Spec.Mode, RuleVersion: frozen.Rule.Version}
}

// SelectedSchemeName 读取冻结方案名称；没有名称时返回 nil，不能用套餐名替代。
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
