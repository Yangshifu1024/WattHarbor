package charging

import "strings"

// 计费方式与设备协议能力的匹配判断属于业务规则：同一种计费方式对不同协议
// 能否成立，由协议声明的能力决定，与存储无关，因此放在领域层。

// CheckModeSupport 判断某个计费方式在指定设备协议下是否可用。
// 返回空串表示可用，否则返回面向运营的拒绝原因。
// 设备端时长计费由固件倒计时并停机，不要求电量或分段功率上报。
func CheckModeSupport(mode ChargeMode, adapter string) string {
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
		// 未知计费方式一律拒绝。放行只会让它在更下游以更隐晦的方式失败，
		// 留在这里报错至少说得清是哪一种计费方式不被认识。
		return "无法识别的计费方式：" + string(mode)
	}
	return ""
}

// ValidDeviceChargeMode 校验设备上报的计费方式取值，空串表示尚未归类。
func ValidDeviceChargeMode(mode string) bool {
	return ChargeMode(strings.TrimSpace(mode)).Valid()
}
