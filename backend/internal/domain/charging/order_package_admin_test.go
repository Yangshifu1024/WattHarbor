package charging

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMigratedAdminOrderPackageKeepsPurchasedOfferSeparateFromSchemePackages 验证原购买内容不被冻结方案中的同号其它套餐覆盖，无 IO。
func TestMigratedAdminOrderPackageKeepsPurchasedOfferSeparateFromSchemePackages(t *testing.T) {
	// 冻结方案中的同号其它套餐不能替换原购买快照。
	rule := Rule{Version: 3, Spec: Spec{Mode: ModeDeviceDuration,
		Scheme: &Scheme{Name: "下单时方案", Packages: []Package{{ID: 2, Name: "其他价格", Mode: "duration", PriceCents: 900, Minutes: 120}}}}}
	offer := Offer{ID: 102, PackageID: 2, StationID: 1, Name: "购买60分钟", Mode: "duration", PriceCents: 100, DurationMinutes: 60}
	raw, err := json.Marshal(map[string]any{"rule": rule, "offer": offer})
	if err != nil {
		t.Fatal(err)
	}
	got := PackageFromSnapshot(raw)
	if got == nil || got.Offer != offer || got.RuleVersion != 3 || got.BillingMode != ModeDeviceDuration || got.Scheme.Name != "下单时方案" {
		t.Fatalf("unexpected frozen package: %+v", got)
	}
	if name := SelectedSchemeName(got); name == nil || *name != "下单时方案" {
		t.Fatalf("selected scheme was replaced by purchased offer name: %v", name)
	}
	if name := SelectedPackageName(got); name == nil || *name != "购买60分钟" {
		t.Fatalf("selected package name did not preserve the purchased offer: %v", name)
	}
}

// TestMigratedAdminSelectedSchemeNameRequiresFrozenValidName 验证无冻结名称保持空且历史长名称完整，无 IO。
func TestMigratedAdminSelectedSchemeNameRequiresFrozenValidName(t *testing.T) {
	for _, selected := range []*OrderPackage{
		nil, {Offer: Offer{Name: "价格档名"}}, {Scheme: &Scheme{Name: "  "}},
	} {
		if name := SelectedSchemeName(selected); name != nil {
			t.Fatalf("invented selected scheme name: %s", *name)
		}
	}
	if name := SelectedSchemeName(&OrderPackage{Scheme: &Scheme{Name: " 下单冻结方案 "}}); name == nil || *name != "下单冻结方案" {
		t.Fatalf("valid frozen scheme name missing: %v", name)
	}
	if name := SelectedSchemeName(&OrderPackage{Scheme: &Scheme{Name: strings.Repeat("历史名称", 20)}}); name == nil || *name != strings.Repeat("历史名称", 20) {
		t.Fatalf("historical frozen scheme name was hidden: %v", name)
	}
}

// TestMigratedAdminSelectedPackageNameUsesPurchasedOfferAndTrimsBlank 验证套餐名来自原购买而非方案名，无 IO。
func TestMigratedAdminSelectedPackageNameUsesPurchasedOfferAndTrimsBlank(t *testing.T) {
	for _, selected := range []*OrderPackage{nil, {}, {Offer: Offer{Name: "  "}, Scheme: &Scheme{Name: "方案名"}}} {
		if name := SelectedPackageName(selected); name != nil {
			t.Fatalf("invented purchased package name: %s", *name)
		}
	}
	if name := SelectedPackageName(&OrderPackage{Offer: Offer{Name: " 1元 "}, Scheme: &Scheme{Name: "金额模式"}}); name == nil || *name != "1元" {
		t.Fatalf("purchased package name changed: %v", name)
	}
	for _, raw := range []string{"", "null", "{", `{}`, `{"rule":{"spec":{"scheme":{"name":"存在方案但无有效套餐"}}}}`} {
		selected := PackageFromSnapshot([]byte(raw))
		if SelectedSchemeName(selected) != nil || SelectedPackageName(selected) != nil {
			t.Fatalf("invalid snapshot invented selection names: %q", raw)
		}
	}
}

// TestMigratedAdminOrderPackageMissingOrInvalidSnapshotDoesNotInventSelection 验证损坏或无价格快照返回空，无 IO。
func TestMigratedAdminOrderPackageMissingOrInvalidSnapshotDoesNotInventSelection(t *testing.T) {
	for _, raw := range []string{"", "null", "{", `{}`, `{"rule":{"version":1}}`, `{"offer":{"name":"无效套餐","price_cents":0}}`} {
		if got := PackageFromSnapshot([]byte(raw)); got != nil {
			t.Fatalf("expected no selected package for %q, got %+v", raw, got)
		}
	}
}

// TestMigratedAdminOrderPackageAllModes 验证金额、时长与电量套餐原字段完整，无 IO。
func TestMigratedAdminOrderPackageAllModes(t *testing.T) {
	for _, offer := range []Offer{
		{ID: 101, StationID: 1, Name: "2元预算", Mode: "amount", PriceCents: 200, MaxMinutes: 600},
		{ID: 102, StationID: 1, Name: "60分钟", Mode: "duration", PriceCents: 100, DurationMinutes: 60},
		{ID: 103, StationID: 1, Name: "2度", Mode: "energy", PriceCents: 220, EnergyWh: 2000},
	} {
		raw, err := json.Marshal(map[string]any{"offer": offer})
		if err != nil {
			t.Fatal(err)
		}
		got := PackageFromSnapshot(raw)
		if got == nil || got.Offer != offer {
			t.Fatalf("lost purchased %s entitlement: %+v", offer.Mode, got)
		}
	}
}
