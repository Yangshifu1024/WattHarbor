package charging

import (
	"encoding/json"
	"testing"
)

// validOffer 造一份通过 Offer.Valid 的最小价格档。
func validOffer() Offer {
	return Offer{ID: 7, StationID: 3, Name: "夜间套餐", Mode: "energy", EnergyWh: 20000, PriceCents: 1500}
}

func snapshotOf(t *testing.T, rule Rule, offer *Offer) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"rule": rule, "offer": offer})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPackageFromSnapshotKeepsFrozenContract(t *testing.T) {
	scheme := &Scheme{Name: "夜间大功率"}
	rule := Rule{Version: 9, Spec: Spec{Scheme: scheme, Mode: ModeServerEnergy}}
	offer := validOffer()

	got := PackageFromSnapshot(snapshotOf(t, rule, &offer))
	if got == nil {
		t.Fatal("合法快照未被还原")
	}
	if got.RuleVersion != 9 || got.BillingMode != ModeServerEnergy {
		t.Fatalf("规则版本/计费方式丢失：%d %s", got.RuleVersion, got.BillingMode)
	}
	if got.Offer.PriceCents != 1500 || got.Offer.Name != "夜间套餐" {
		t.Fatalf("价格档被改动：%+v", got.Offer)
	}
	if got.Scheme == nil || got.Scheme.Name != "夜间大功率" {
		t.Fatalf("冻结方案丢失：%+v", got.Scheme)
	}
}

func TestPackageFromSnapshotRefusesToSubstituteLiveTemplate(t *testing.T) {
	rule := Rule{Version: 3, Spec: Spec{Mode: ModeServerEnergy}}
	// 快照缺少套餐、损坏或套餐无效时都返回 nil，不能用当前模板补齐历史价格。
	broken := validOffer()
	broken.PriceCents = 0 // Offer.Valid 要求价格大于 0
	for name, raw := range map[string][]byte{
		"空快照":      nil,
		"非法 JSON":  []byte("{not json"),
		"缺 offer":  snapshotOf(t, rule, nil),
		"offer 非法": snapshotOf(t, rule, &broken),
	} {
		if got := PackageFromSnapshot(raw); got != nil {
			t.Fatalf("%s 竟被还原成 %+v", name, got)
		}
	}
}

func TestSelectedNamesNeverSubstituteOfferForScheme(t *testing.T) {
	// 方案名缺失时必须返回 nil，不能拿 offer 的"夜间套餐"顶替成方案名。
	pkg := &OrderPackage{Offer: validOffer(), Scheme: nil}
	if SelectedSchemeName(pkg) != nil {
		t.Fatal("无方案时不应返回方案名")
	}
	blank := &OrderPackage{Offer: validOffer(), Scheme: &Scheme{Name: "   "}}
	if SelectedSchemeName(blank) != nil {
		t.Fatal("空白方案名应归为空")
	}
	// 方案名与价格档名是两样东西，各读各的。
	named := &OrderPackage{Offer: validOffer(), Scheme: &Scheme{Name: "夜间大功率"}}
	if *SelectedSchemeName(named) != "夜间大功率" {
		t.Fatal("方案名读错")
	}
	if *SelectedPackageName(named) != "夜间套餐" {
		t.Fatal("价格档名读错")
	}
	if SelectedSchemeName(nil) != nil || SelectedPackageName(nil) != nil {
		t.Fatal("空口径时两者都应为 nil")
	}
}
