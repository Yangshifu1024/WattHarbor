//go:build integration

package pricing

import (
	"encoding/json"
	"errors"
	"testing"

	"wattharbor/internal/device"
	"wattharbor/internal/domain/charging"
	"wattharbor/internal/testfixture"
)

// TestImportCompatibilityCachesAbsentRuleAndChecksWholeScheme 验证无站点规则时可导入，有规则时检查全部套餐和算法。
// 使用真实规则表，外层事务回滚。
func TestImportCompatibilityCachesAbsentRuleAndChecksWholeScheme(t *testing.T) {
	db := pricingConn(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	station := testfixture.NewID()
	devices := []device.ImportDevice{{DeviceID: "IMPORTCOMPAT1", StationID: station, ProtocolAdapter: "dc589"}, {DeviceID: "IMPORTCOMPAT2", StationID: station, ProtocolAdapter: "dc589"}}
	repo := Repo{DB: tx}
	if err := repo.CheckImportAgainstStation(tx, devices); err != nil {
		t.Fatalf("absent rule for two devices: %v", err)
	}
	scheme := pricingScheme()
	raw, err := json.Marshal(charging.Spec{Scheme: &scheme})
	if err != nil {
		t.Fatal(err)
	}
	row := map[string]any{"name": "import compatibility proof", "station_id": station, "spec_json": string(raw), "channel": "default", "version": 1, "status": "active"}
	if err := tx.Table("pricing_rule").Create(row).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.CheckImportAgainstStation(tx, devices); err != nil {
		t.Fatalf("complete scheme without legacy mode: %v", err)
	}
	// 首个时长套餐兼容；第二个超出 DC589 的 4320 分钟上限，仍必须阻止导入。
	scheme.Packages = append(scheme.Packages, charging.Package{ID: 2, Name: "unsupported", Mode: "duration", Minutes: 4321, PriceCents: 1})
	raw, err = json.Marshal(charging.Spec{Mode: "duration", Scheme: &scheme})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Table("pricing_rule").Where("station_id=?", station).Update("spec_json", string(raw)).Error; err != nil {
		t.Fatal(err)
	}
	var blocked *ErrMeteringBlocked
	if err := repo.CheckImportAgainstStation(tx, devices); !errors.As(err, &blocked) {
		t.Fatalf("later incompatible package accepted: %v", err)
	}
}
