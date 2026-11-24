package dc589

import (
	"fmt"
	"strconv"
	"strings"

	"wattharbor/internal/domain/devices"
	"wattharbor/internal/protocol"
)

// FaultMetric 按故障码返回指标名、严重程度和说明。
// 端口 0xFF 表示整机故障；其余故障按端口分别记录。
// severity 对应 central alert_event.severity，本适配器只产出 fatal 与 critical，
// warning 无生产写入路径；warning 取值在 devices.AlertSeverity 中保留以覆盖列约束。
func FaultMetric(event protocol.Event) (metric string, severity devices.AlertSeverity, note string) {
	if event.Port == 0xff {
		switch event.FaultCode {
		case 0xbb:
			return "smoke", devices.AlertSeverityFatal, "设备上报烟雾告警（0xBB）"
		case 0xaa:
			return "high_temperature", devices.AlertSeverityCritical, "设备上报高温告警（0xAA）"
		}
		return "device_fault", devices.AlertSeverityCritical, fmt.Sprintf("设备上报故障（0x%02X）", event.FaultCode)
	}
	return fmt.Sprintf("port_fault_%d", event.Port), devices.AlertSeverityCritical, fmt.Sprintf("端口 %d 上报故障（0x%02X）", event.Port, event.FaultCode)
}

// FaultRecovered 依据故障之后的最新心跳判断告警是否已恢复：
// 设备级告警要求设备状态归零，端口告警要求端口状态不大于 2。
// 没有端口状态的心跳仍视为未恢复。
func FaultRecovered(metric string, heartbeat protocol.Event) bool {
	if heartbeat.PortStates == nil {
		return false
	}
	if metric == "smoke" || metric == "high_temperature" || metric == "device_fault" {
		return heartbeat.DeviceStatus == 0
	}
	port, err := strconv.Atoi(strings.TrimPrefix(metric, "port_fault_"))
	if err != nil || port < 1 || port > len(heartbeat.PortStates) {
		return false
	}
	state := heartbeat.PortStates[port-1]
	return state <= 2
}
