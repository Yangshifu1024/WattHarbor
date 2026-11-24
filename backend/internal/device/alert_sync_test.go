package device

import (
	"encoding/json"
	"testing"
	"wattharbor/internal/protocol"

	"wattharbor/internal/protocol/dc589"
)

// TestRecoveryRequiresExplicitNormalStates 验证缺字段或 null 不会因零值解码被当作正常设备或空闲端口。
func TestRecoveryRequiresExplicitNormalStates(t *testing.T) {
	for _, input := range []struct {
		// metric、payload 指定恢复指标和协议 JSON，检查缺字段或 null 不能自动变为正常状态。
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
	// protocol.Event 的 []uint8 经 encoding/json 编码为 base64 字符串，测试须使用真实保存格式。
	raw, _ := json.Marshal(protocol.Event{DeviceStatus: 0, PortStates: []uint8{0}})
	event, err = decodeRecoveryPayload(string(raw), "smoke")
	if err != nil || !dc589.FaultRecovered("smoke", event) {
		t.Fatalf("original base64=%s %v", raw, err)
	}
}
