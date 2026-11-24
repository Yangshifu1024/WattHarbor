package session

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"chargepilot/internal/protocol"
)

type fakeSession struct {
	closed atomic.Bool
	sent   atomic.Int32
}

func (f *fakeSession) Send(context.Context, protocol.Command) error {
	if f.closed.Load() {
		return errors.New("closed")
	}
	f.sent.Add(1)
	return nil
}
func (f *fakeSession) Close() error { f.closed.Store(true); return nil }

func TestReconnectRetiresOldSessionWithoutLosingReplacement(t *testing.T) {
	registry := &Registry{}
	old := &fakeSession{}
	var replaced atomic.Int32
	detachOld := registry.Attach("board", old, func(protocol.Session) { replaced.Add(1) })
	current := &fakeSession{}
	detachCurrent := registry.Attach("board", current, func(protocol.Session) { replaced.Add(1) })
	if !old.closed.Load() {
		t.Fatal("previous connection stayed open")
	}
	if replaced.Load() != 1 {
		t.Fatalf("replacement callback ran %d times, want 1 (only the displaced session)", replaced.Load())
	}
	detachOld()
	if err := registry.Send(context.Background(), "board", protocol.Command{Kind: protocol.CommandStop}); err != nil || current.sent.Load() != 1 {
		t.Fatalf("replacement lost: sent=%d err=%v", current.sent.Load(), err)
	}
	detachCurrent()
	if err := registry.Send(context.Background(), "board", protocol.Command{Kind: protocol.CommandStop}); !errors.Is(err, ErrOffline) {
		t.Fatalf("detached connection still routable: %v", err)
	}
}

// TestDeviceOperationKeepsOwnershipUntilSendReturns 验证同设备不能跨越尚未完成的发送，不同设备可独立执行。
func TestDeviceOperationKeepsOwnershipUntilSendReturns(t *testing.T) {
	registry := &Registry{}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	defer close(release)
	go func() {
		done <- registry.WithDeviceOperation(t.Context(), "board", func() error { close(entered); <-release; return errors.New("write failed") })
	}()
	<-entered
	other := false
	if err := registry.WithDeviceOperation(t.Context(), "other", func() error { other = true; return nil }); err != nil || !other {
		t.Fatal("different device blocked", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	if err := registry.WithDeviceOperation(ctx, "board", func() error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled waiter ran=%v err=%v", called, err)
	}
	registry.mu.RLock()
	users := registry.operations["board"].refs
	registry.mu.RUnlock()
	if users != 1 {
		t.Fatalf("cancelled waiter leaked users=%d", users)
	}
	// 第二项实际等待已持有的许可；用内部引用计数确认已进入等待，取消只控制确定性闸门。
	waiting := make(chan error, 1)
	go func() {
		waiting <- registry.WithDeviceOperation(t.Context(), "board", func() error { called = true; return nil })
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		registry.mu.RLock()
		users = registry.operations["board"].refs
		registry.mu.RUnlock()
		if users == 2 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("second operation did not wait")
		default:
			runtime.Gosched()
		}
	}
	if called {
		t.Fatal("second operation crossed running send")
	}
	release <- struct{}{}
	if err := <-done; err == nil {
		t.Fatal("write error lost")
	}
	if err := <-waiting; err != nil || !called {
		t.Fatalf("second operation=%v called=%v", err, called)
	}
	registry.mu.RLock()
	remaining := len(registry.operations)
	registry.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("operation entries leaked: %d", remaining)
	}
}

// callbackSink 为回执许可测试记录真实委托次数，不提供数据库或网络副作用。
type callbackSink struct {
	records  atomic.Int32
	sessions atomic.Int32
}

// Register 此测试只关注回执和连接能力，注册返回成功。
func (*callbackSink) Register(context.Context, protocol.Registration) error { return nil }

// Record 记录回调次数，供断言取消时未越过设备许可。
func (s *callbackSink) Record(context.Context, protocol.Event) error { s.records.Add(1); return nil }

// HasChargingPorts 保留底层心跳选择结果。
func (*callbackSink) HasChargingPorts(context.Context, string) (bool, error) { return true, nil }

// RecordSession 保留底层会话统计委托。
func (s *callbackSink) RecordSession(context.Context, protocol.SessionRecord) error {
	s.sessions.Add(1)
	return nil
}

// TestSerializedSinkGuardsAcknowledgementsAndPreservesCapabilities 取消的两类 ACK 不写库，普通设备事件与会话能力不丢失。
func TestSerializedSinkGuardsAcknowledgementsAndPreservesCapabilities(t *testing.T) {
	registry := &Registry{}
	base := &callbackSink{}
	sink := SerializedSink{Registry: registry, Sink: base}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- registry.WithDeviceOperation(t.Context(), "board", func() error { close(entered); <-release; return nil })
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for _, kind := range []protocol.EventType{protocol.StartResult, protocol.StopResult} {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := sink.Record(ctx, protocol.Event{Type: kind, DeviceID: "board"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("ACK %s bypassed gate: %v", kind, err)
		}
	}
	if base.records.Load() != 0 {
		t.Fatal("cancelled acknowledgement persisted")
	}
	if err := sink.Record(t.Context(), protocol.Event{Type: protocol.Heartbeat, DeviceID: "board"}); err != nil || base.records.Load() != 1 {
		t.Fatal("device event capability lost", err)
	}
	if charging, err := sink.HasChargingPorts(t.Context(), "board"); err != nil || !charging {
		t.Fatal("reconnect capability lost", err)
	}
	if err := sink.RecordSession(t.Context(), protocol.SessionRecord{}); err != nil || base.sessions.Load() != 1 {
		t.Fatal("session capability lost", err)
	}
}
