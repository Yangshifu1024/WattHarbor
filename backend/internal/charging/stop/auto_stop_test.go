package stop

import (
	"testing"
	"time"

	"chargepilot/internal/protocol"
)

// TestNoPowerRequiresContinuousMinuteAndFreshHeartbeat 验证零功率须连续一分钟且末条心跳有效。
// 正功率重置计时，缺少目标端口不能算作零功率。
func TestNoPowerRequiresContinuousMinuteAndFreshHeartbeat(t *testing.T) {
	now := time.Now().UTC()
	at := func(before time.Duration, power uint32) protocol.Event {
		return protocol.Event{Type: protocol.Heartbeat, ReceivedAt: now.Add(-before), ChargingPorts: []protocol.PortTelemetry{{Port: 1, PowerDeciWatts: power}}}
	}
	if noPowerForMinute([]protocol.Event{at(70*time.Second, 200), at(50*time.Second, 0), at(2*time.Second, 0)}, 1, now) {
		t.Fatal("stopped before full minute of zero power")
	}
	if !noPowerForMinute([]protocol.Event{at(90*time.Second, 200), at(70*time.Second, 0), at(55*time.Second, 0), at(40*time.Second, 0), at(25*time.Second, 0), at(10*time.Second, 0), at(2*time.Second, 0)}, 1, now) {
		t.Fatal("did not stop after a full minute")
	}
	if noPowerForMinute([]protocol.Event{at(90*time.Second, 200), at(70*time.Second, 0), at(40*time.Second, 0)}, 1, now) {
		t.Fatal("stale heartbeat must not imply unplug")
	}
	if noPowerForMinute([]protocol.Event{at(90*time.Second, 0), at(20*time.Second, 200), at(2*time.Second, 0)}, 1, now) {
		t.Fatal("positive power must reset the timer")
	}
	other := protocol.Event{Type: protocol.Heartbeat, ReceivedAt: now.Add(-2 * time.Second), ChargingPorts: []protocol.PortTelemetry{{Port: 2, PowerDeciWatts: 0}}}
	if noPowerForMinute([]protocol.Event{at(90*time.Second, 0), at(70*time.Second, 0), other}, 1, now) {
		t.Fatal("a heartbeat without this port is not zero-power evidence")
	}
}

// TestNoPowerRejectsInterruptedEvidence 验证静默、乱序及缺失事实不能补足一分钟。
func TestNoPowerRejectsInterruptedEvidence(t *testing.T) {
	now := time.Now().UTC()
	at := func(seconds int, ports ...protocol.PortTelemetry) protocol.Event {
		return protocol.Event{Type: protocol.Heartbeat, ReceivedAt: now.Add(time.Duration(seconds) * time.Second), ChargingPorts: ports}
	}
	z := protocol.PortTelemetry{Port: 1}
	for _, tc := range []struct {
		name string
		// events 按给定顺序构造的心跳事实，用于检查静默、缺端口或时间中断不能满足连续零功率。
		events []protocol.Event
	}{
		{"reconnect", []protocol.Event{at(-70, z), at(-2, z)}},
		{"missing port", []protocol.Event{at(-70, z), at(-55, z), at(-40), at(-25, z), at(-10, z), at(-2, z)}},
		{"out of order", []protocol.Event{at(-70, z), at(-40, z), at(-55, z), at(-25, z), at(-10, z), at(-2, z)}},
		{"future", []protocol.Event{at(-70, z), at(-55, z), at(-40, z), at(-25, z), at(-10, z), at(2, z)}},
		{"duplicate time", []protocol.Event{at(-70, z), at(-55, z), at(-55, z), at(-40, z), at(-25, z), at(-10, z), at(-2, z)}},
		{"silence is not a sample", []protocol.Event{at(-60, z), at(-45, z), at(-30, z), at(-15, z), at(-2, z)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if noPowerForMinute(tc.events, 1, now) {
				t.Fatal("interrupted evidence triggered STOP")
			}
		})
	}
}
