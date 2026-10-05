package device

import (
	"encoding/json"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol/dc589"
)

// TestRecoveryRequiresExplicitNormalStates 只验证纯协议解码的存在性，缺字段和null不能被零值解码为正常设备或端口。
func TestRecoveryRequiresExplicitNormalStates(t *testing.T) {
	for _, input := range []struct {
		// metric,payload metric 为待恢复告警指标，payload 为原协议 JSON；组合用于确认缺字段或 null 不得隐式解码为正常状态。
		metric, payload string
	}{
		{"smoke", `{"PortStates":[0]}`}, {"high_temperature", `{"DeviceStatus":null,"PortStates":[0]}`},
		{"port_fault_1", `{"DeviceStatus":0,"PortStates":[null]}`},
	} {
		if _, err := decodeRecoveryPayload(input.payload, input.metric); err == nil {
			t.Fatal(input)
		}
	}
	for _, metric := range []string{"smoke", "high_temperature", "device_fault", "port_fault_1"} {
		event, err := decodeRecoveryPayload(`{"DeviceStatus":0,"PortStates":[0]}`, metric)
		if err != nil || !dc589.FaultRecovered(metric, event) {
			t.Fatalf("explicit zero=%s %v", metric, err)
		}
	}
	event, err := decodeRecoveryPayload(`{"DeviceStatus":0,"PortStates":null}`, "smoke")
	if err != nil || dc589.FaultRecovered("smoke", event) {
		t.Fatal("missing ports recovered")
	}
	// protocol.Event的[]uint8使用encoding/json原base64字符串，必须与真实持久载荷一起回归。
	raw, _ := json.Marshal(protocol.Event{DeviceStatus: 0, PortStates: []uint8{0}})
	event, err = decodeRecoveryPayload(string(raw), "smoke")
	if err != nil || !dc589.FaultRecovered("smoke", event) {
		t.Fatalf("original base64=%s %v", raw, err)
	}
}
