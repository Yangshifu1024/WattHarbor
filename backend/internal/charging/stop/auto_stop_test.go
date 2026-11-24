package stop

import (
	"testing"
	"time"

	"wattharbor/internal/protocol"
)

// TestNoPowerRecoversOnlyAfterIndependentWindow 验证旧重复和乱序不永久阻断，异常前累计不能补入新窗口。
func TestNoPowerRecoversOnlyAfterIndependentWindow(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	at := func(seconds int) protocol.Event {
		return protocol.Event{Type: protocol.Heartbeat, ReceivedAt: now.Add(time.Duration(seconds) * time.Second), ChargingPorts: []protocol.PortTelemetry{{Port: 1}}}
	}
	clean := []protocol.Event{at(-70), at(-55), at(-40), at(-25), at(-10), at(-2)}
	for _, tc := range []struct {
		name   string           // 恢复场景名称
		prefix []protocol.Event // 不可拼接的历史证据
		want   bool             // 新窗口是否可证明完整一分钟
	}{
		{"old duplicate", []protocol.Event{at(-7200), at(-7200)}, true},
		{"old disorder", []protocol.Event{at(-7200), at(-7215)}, true},
		{"recent duplicate", []protocol.Event{at(-70), at(-70)}, false},
		{"disorder cannot move baseline", []protocol.Event{at(-20), at(-7200)}, false},
		{"future invalidates input", []protocol.Event{at(1)}, false},
		{"zero time invalidates input", []protocol.Event{{Type: protocol.Heartbeat}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			samples := append(append([]protocol.Event{}, tc.prefix...), clean...)
			if got := noPowerForMinute(samples, 1, now); got != tc.want {
				t.Fatalf("got=%v want=%v", got, tc.want)
			}
		})
	}
}

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

// TestNoPowerRejectsInterruptedEvidence 检查未上报、乱序及端口缺失不能补足一分钟。
func TestNoPowerRejectsInterruptedEvidence(t *testing.T) {
	now := time.Now().UTC()
	at := func(seconds int, ports ...protocol.PortTelemetry) protocol.Event {
		return protocol.Event{Type: protocol.Heartbeat, ReceivedAt: now.Add(time.Duration(seconds) * time.Second), ChargingPorts: ports}
	}
	z := protocol.PortTelemetry{Port: 1}
	for _, tc := range []struct {
		name string
		// events 按给定顺序构造的心跳，用于检查静默、缺端口或时间中断不能满足连续零功率。
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
