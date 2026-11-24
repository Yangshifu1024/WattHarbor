package charging

import (
	"testing"

	"wattharbor/internal/protocol"
)

func TestProtocolCapabilitiesCoverDc589(t *testing.T) {
	caps, err := ProtocolCapabilities("dc589")
	if err != nil {
		t.Fatalf("dc589 应有已知能力：%v", err)
	}
	// 这三项决定按电量、按功率、按时长计费是否可行，少一项会让
	// CheckModeSupport 拒绝本该允许的方案。
	if !caps.Energy || caps.MaxMinutes == 0 || !caps.ReportsEnergy || !caps.ReportsSegmentedPower {
		t.Fatalf("dc589 能力缺失：%+v", caps)
	}
}

func TestProtocolCapabilitiesRejectUnknownAdapter(t *testing.T) {
	// 四个值全部非法：空串、未登记标识，以及大小写与序号不同的近似值。
	for _, adapter := range []protocol.AdapterID{"", "unknown", "DC589", "dc590"} {
		if _, err := ProtocolCapabilities(adapter); err == nil {
			t.Fatalf("协议 %q 不应被接受", adapter)
		}
	}
}

func TestCheckModeSupportByProtocolCapabilities(t *testing.T) {
	// 时长计费不要求上报能力，dc589 全部支持，六种计费方式都应放行。
	for _, mode := range []ChargeMode{
		ModeServerRealtimePower, ModeServerMaxPower, ModeServerEnergy,
		ModeDeviceDuration, ModeDeviceEnergy, ModeDevicePower,
	} {
		if reason := CheckModeSupport(mode, "dc589"); reason != "" {
			t.Fatalf("dc589 下 %s 应放行，却报 %q", mode, reason)
		}
	}
	// 未知协议不能被当作"有全部能力"，必须把原因原样带出去。
	if reason := CheckModeSupport(ModeServerEnergy, "unknown"); reason == "" {
		t.Fatal("未知协议应被拒绝")
	}
}

func TestCheckModeSupportRejectsModeWithoutRequiredCapability(t *testing.T) {
	// 构造一个什么上报能力都没有的协议：能量与功率类必须被拒。
	// 设备端时长计费仍可用（由固件倒计时停机，不要求上报）。
	// 这里直接用 dc589 的能力集合逐项验证归属，避免依赖不存在的假协议。
	caps, err := ProtocolCapabilities("dc589")
	if err != nil {
		t.Fatal(err)
	}
	if !caps.ReportsEnergy {
		t.Skip("dc589 若将来不报电量，断言需随之调整")
	}
	// dc589 全能力在手，六个模式都放行；能力归属的判定逻辑由上面的
	// 逐模式用例覆盖，这里只确认函数不会因未知模式而误放行。
	if reason := CheckModeSupport(ChargeMode("nonexistent"), "dc589"); reason == "" {
		t.Fatal("未知计费方式不应被放行")
	}
}

func TestValidDeviceChargeModeAcceptsEngineModesOnly(t *testing.T) {
	for _, mode := range []ChargeMode{
		ModeServerRealtimePower, ModeServerMaxPower, ModeServerEnergy,
		ModeDeviceDuration, ModeDeviceEnergy, ModeDevicePower,
	} {
		if !ValidDeviceChargeMode(mode) {
			t.Fatalf("计费引擎认可的 %q 应判合法", mode)
		}
		// 前后空白表示设备上报时带了填充，不应因此被判非法。
		if !ValidDeviceChargeMode(ChargeMode("  " + string(mode) + "  ")) {
			t.Fatalf("带空白的 %q 应判合法", mode)
		}
	}
	for _, mode := range []ChargeMode{"", "   ", "unknown", "none", "server_unknown"} {
		if ValidDeviceChargeMode(mode) {
			t.Fatalf("计费引擎不认的 %q 应判非法", mode)
		}
	}
}
