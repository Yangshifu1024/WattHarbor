package charging

import (
	"fmt"
)

// ProtocolCapabilities defines the adapter contract. Provisioning with this
// protocol establishes compatibility; operators cannot override its abilities.
func ProtocolCapabilities(adapter string) (Capabilities, error) {
	switch adapter {
	case "dc589":
		return Capabilities{Duration: true, Energy: true, MaxMinutes: 4320,
			OnlineCard: true, StopWhenFull: true, ReportsEnergy: true, ReportsSegmentedPower: true}, nil
	default:
		return Capabilities{}, fmt.Errorf("不支持的设备协议：%s", adapter)
	}
}
