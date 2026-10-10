package charging

import (
	"fmt"

	"chargepilot/internal/protocol"
)

// ProtocolCapabilities 定义协议适配器的能力；运营配置不能覆盖协议本身的能力。
func ProtocolCapabilities(adapter protocol.AdapterID) (Capabilities, error) {
	switch adapter {
	case protocol.AdapterIDDC589:
		return Capabilities{Duration: true, Energy: true, MaxMinutes: 4320,
			OnlineCard: true, StopWhenFull: true, ReportsEnergy: true, ReportsSegmentedPower: true}, nil
	default:
		return Capabilities{}, fmt.Errorf("不支持的设备协议：%s", adapter)
	}
}
