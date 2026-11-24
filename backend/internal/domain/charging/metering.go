package charging

import (
	"strings"

	"wattharbor/internal/protocol"
)

// 计费方式是否可用由协议能力决定，与数据库配置无关。

// CheckModeSupport 判断某个计费方式在指定设备协议下是否可用。
// 返回空串表示可用，否则返回面向运营的拒绝原因。
// 设备端时长计费由固件倒计时并停机，不要求电量或分段功率上报。
func CheckModeSupport(mode ChargeMode, adapter protocol.AdapterID) string {
	abilities, err := ProtocolCapabilities(adapter)
	if err != nil {
		return err.Error()
	}
	switch mode {
	case ModeDeviceDuration:
		return ""
	case ModeServerEnergy, ModeDeviceEnergy:
		if !abilities.ReportsEnergy {
			return "设备协议不支持电量上报，无法按电量计费"
		}
	case ModeServerRealtimePower, ModeServerMaxPower, ModeDevicePower:
		if !abilities.ReportsSegmentedPower {
			return "设备协议不支持分段功率上报，无法按功率计费"
		}
	default:
		// 未知计费方式直接拒绝，返回明确原因。
		return "无法识别的计费方式：" + string(mode)
	}
	return ""
}

// ValidDeviceChargeMode 校验设备上报的计费方式取值，空串表示尚未归类。
func ValidDeviceChargeMode(mode ChargeMode) bool {
	return ChargeMode(strings.TrimSpace(string(mode))).Valid()
}
